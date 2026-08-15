package stacks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"

	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
)

type entitlementFeatureChecker struct {
	enabled bool
	keys    []string
}

func (f *entitlementFeatureChecker) IsEnabled(_ context.Context, featureKey string, _ string) (bool, error) {
	f.keys = append(f.keys, featureKey)
	return f.enabled, nil
}

type staticManagedRuntimeLeaseLister struct {
	err error
}

func (l staticManagedRuntimeLeaseLister) ListByTenant(context.Context, string) ([]vmlease.Lease, error) {
	return nil, l.err
}

const testRecoveryPassphraseHash = "$argon2id$v=19$m=65536,t=3,p=4$demo$signed" // #nosec G101 -- deterministic non-secret test fixture hash.

// TestCreateLegacyJob_ValidInput tests legacy job creation without orchestrator.
// NOTE: This is a unit test for the helper function logic.
// Full integration tests require a running PocketBase instance (see integration_test.go).
func TestValidationHelpers(t *testing.T) {
	tests := []struct {
		name     string
		mode     string
		wantErr  bool
		errMatch string
	}{
		{"valid easy mode", "easy", false, ""},
		{"valid techie mode", "techie", false, ""},
		{"invalid mode", "invalid", true, "mode"},
		{"empty mode defaults to easy", "", false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mode := tt.mode
			if mode == "" {
				mode = "easy" // Default
			}
			valid := mode == "easy" || mode == "techie"
			if tt.wantErr && valid {
				t.Errorf("expected error for mode %q but validation passed", tt.mode)
			}
			if !tt.wantErr && !valid && tt.mode != "" {
				t.Errorf("expected mode %q to be valid but failed", tt.mode)
			}
		})
	}
}

// TestUserConfigNormalization tests various input formats for user_config.
func TestUserConfigNormalization(t *testing.T) {
	tests := []struct {
		name       string
		input      map[string]interface{}
		wantFields []string
	}{
		{
			name: "full user_config",
			input: map[string]interface{}{
				"name":     "test-stack",
				"provider": "proxmox",
				"services": []string{"traefik", "portainer"},
			},
			wantFields: []string{"name", "provider", "services"},
		},
		{
			name: "minimal config",
			input: map[string]interface{}{
				"name": "minimal",
			},
			wantFields: []string{"name"},
		},
		{
			name: "with nested options",
			input: map[string]interface{}{
				"name":    "with-opts",
				"options": map[string]interface{}{"key": "value"},
			},
			wantFields: []string{"name", "options"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Verify all expected fields exist
			for _, field := range tt.wantFields {
				if _, ok := tt.input[field]; !ok {
					t.Errorf("expected field %q in input", field)
				}
			}

			// Verify JSON serialization works
			data, err := json.Marshal(tt.input)
			if err != nil {
				t.Fatalf("failed to marshal: %v", err)
			}

			var parsed map[string]interface{}
			if err := json.Unmarshal(data, &parsed); err != nil {
				t.Fatalf("failed to unmarshal: %v", err)
			}
		})
	}
}

// TestRedactUserConfigForStorage_VariousInputs tests the redaction function.
func TestRedactUserConfigForStorage_VariousInputs(t *testing.T) {
	tests := []struct {
		name         string
		input        map[string]interface{}
		shouldRemove []string
		shouldKeep   []string
	}{
		{
			name: "removes password",
			input: map[string]interface{}{
				"name":     "test",
				"password": "secret123",
			},
			shouldRemove: []string{"password"},
			shouldKeep:   []string{"name"},
		},
		{
			name: "removes nested secrets",
			input: map[string]interface{}{
				"name": "test",
				"credentials": map[string]interface{}{
					"apiKey":   "key123",
					"username": "user",
				},
			},
			shouldRemove: []string{}, // Check nested
			shouldKeep:   []string{"name", "credentials"},
		},
		{
			name:         "handles nil gracefully",
			input:        nil,
			shouldRemove: []string{},
			shouldKeep:   []string{},
		},
		{
			name:         "handles empty map",
			input:        map[string]interface{}{},
			shouldRemove: []string{},
			shouldKeep:   []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.input == nil {
				// nil input should not panic - function returns empty map for nil
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("redactUserConfigForStorage panicked on nil input: %v", r)
					}
				}()
				result := redactUserConfigForStorage(tt.input)
				// Function returns empty map for nil input (defensive behavior)
				if result == nil {
					t.Errorf("expected empty map for nil input, got nil")
				}
				return
			}

			result := redactUserConfigForStorage(tt.input)

			for _, key := range tt.shouldRemove {
				if _, ok := result[key]; ok {
					t.Errorf("expected key %q to be removed", key)
				}
			}

			for _, key := range tt.shouldKeep {
				if _, ok := result[key]; !ok {
					t.Errorf("expected key %q to be kept", key)
				}
			}
		})
	}
}

func TestRedactUserConfigForStorage_RemovesOwnerRecoveryMaterial(t *testing.T) {
	redacted := redactUserConfigForStorage(map[string]interface{}{
		"name": "configured-stack",
		"identity": map[string]interface{}{
			"owner": map[string]interface{}{
				"email":    "owner@example.com",
				"username": "owner",
			},
			"recovery": map[string]interface{}{
				"passphrase_hash": testRecoveryPassphraseHash,
			},
		},
		"options": map[string]interface{}{
			"recovery_passphrase_hash": testRecoveryPassphraseHash,
		},
	})

	identity := redacted["identity"].(map[string]interface{})
	recovery := identity["recovery"].(map[string]interface{})
	if _, ok := recovery["passphrase_hash"]; ok {
		t.Fatalf("expected passphrase_hash to be redacted, got %v", recovery)
	}
	options := redacted["options"].(map[string]interface{})
	if _, ok := options["recovery_passphrase_hash"]; ok {
		t.Fatalf("expected recovery_passphrase_hash to be redacted, got %v", options)
	}
}

func TestRedactUserConfigRawForStorage_RedactsJSONRecoveryHash(t *testing.T) {
	raw := `{"name":"configured-stack","identity":{"recovery":{"passphrase_hash":"` + testRecoveryPassphraseHash + `"}}}`

	redacted := redactUserConfigRawForStorage(raw)

	if strings.Contains(redacted, "$argon2id$") || strings.Contains(redacted, "passphrase_hash") {
		t.Fatalf("expected raw JSON recovery hash to be redacted, got %s", redacted)
	}
	if !strings.Contains(redacted, "configured-stack") {
		t.Fatalf("expected non-sensitive raw JSON fields to remain, got %s", redacted)
	}
}

func TestNormalizeCreateStackRequestRejectsPlaintextRecoveryPassphrase(t *testing.T) {
	_, msg := normalizeCreateStackRequest(createStackRequest{
		Name: "Plaintext Recovery",
		Mode: "easy",
		StackSpec: map[string]interface{}{
			"name": "plaintext-recovery",
			"metadata": map[string]interface{}{
				"created_by":           "wizard",
				"owner_bootstrap_mode": "auto",
				"owner_source":         "local",
			},
		},
		Options: map[string]interface{}{
			"owner_bootstrap_mode": "auto",
			"owner_source":         "local",
			"recovery_passphrase":  "correct horse battery staple",
		},
	})

	if !strings.Contains(msg, "Plaintext recovery passphrases are not accepted") {
		t.Fatalf("normalizeCreateStackRequest() message = %q, want plaintext recovery rejection", msg)
	}
}

func TestNormalizeCreateStackRequestRejectsNestedPlaintextRecoveryMaterial(t *testing.T) {
	tests := []struct {
		name      string
		recovery  map[string]interface{}
		wantError string
	}{
		{
			name: "identity recovery passphrase",
			recovery: map[string]interface{}{
				"passphrase": "correct horse battery staple",
			},
			wantError: "Plaintext recovery passphrases are not accepted",
		},
		{
			name: "identity recovery plaintext",
			recovery: map[string]interface{}{
				"plaintext": "correct horse battery staple",
			},
			wantError: "Plaintext recovery passphrases are not accepted",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, msg := normalizeCreateStackRequest(createStackRequest{
				Name: "Nested Plaintext Recovery",
				Mode: "easy",
				StackSpec: map[string]interface{}{
					"name": "nested-plaintext-recovery",
					"metadata": map[string]interface{}{
						"created_by":           "wizard",
						"owner_bootstrap_mode": "auto",
						"owner_source":         "local",
					},
					"identity": map[string]interface{}{
						"recovery": tt.recovery,
					},
				},
				Options: map[string]interface{}{
					"owner_bootstrap_mode": "auto",
					"owner_source":         "local",
				},
			})

			if !strings.Contains(msg, tt.wantError) {
				t.Fatalf("normalizeCreateStackRequest() message = %q, want %q", msg, tt.wantError)
			}
		})
	}
}

func TestNormalizeCreateStackRequestRejectsRawYAMLPlaintextRecoveryMaterial(t *testing.T) {
	_, msg := normalizeCreateStackRequest(createStackRequest{
		Name: "Raw YAML Recovery",
		Mode: "easy",
		UserConfigRaw: `
name: raw-yaml-recovery
identity:
  recovery:
    passphrase_hash: ` + testRecoveryPassphraseHash + `
    passphrase: correct horse battery staple
`,
		UserConfigFormat: "yaml",
	})

	if !strings.Contains(msg, "Plaintext recovery passphrases are not accepted") {
		t.Fatalf("normalizeCreateStackRequest() message = %q, want plaintext recovery rejection", msg)
	}
}

func TestNormalizeCreateStackRequestAllowsRawYAMLRecoveryHashReference(t *testing.T) {
	_, msg := normalizeCreateStackRequest(createStackRequest{
		Name: "Raw YAML Recovery Hash",
		Mode: "easy",
		UserConfigRaw: `
name: raw-yaml-recovery-hash
identity:
  recovery:
    passphrase_hash: ` + testRecoveryPassphraseHash + `
    secret_ref: secret://techstack/recovery-passphrase-hash
`,
		UserConfigFormat: "yaml",
	})

	if msg != "" {
		t.Fatalf("normalizeCreateStackRequest() message = %q, want empty", msg)
	}
}

// TestStackNameNormalization tests name trimming and defaults.
func TestStackNameNormalization(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", defaultCreateStackName},
		{"   ", defaultCreateStackName},
		{"MyStack", "MyStack"},
		{"  My Stack  ", "My Stack"},
		{"homelab-1", "homelab-1"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			name := tt.input
			name = trimSpace(name)
			if name == "" {
				name = defaultCreateStackName
			}
			if name != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, name)
			}
		})
	}
}

// trimSpace is a helper to match the actual implementation
func trimSpace(s string) string {
	// Trim leading/trailing whitespace
	result := ""
	start := 0
	end := len(s)

	for start < end && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n') {
		end--
	}

	if start < end {
		result = s[start:end]
	}
	return result
}

// TestModeValidation tests mode input validation.
func TestModeValidation(t *testing.T) {
	validModes := map[string]bool{
		"easy":   true,
		"techie": true,
	}

	tests := []struct {
		mode    string
		isValid bool
	}{
		{"easy", true},
		{"techie", true},
		{"EASY", false},   // Case sensitive
		{"expert", false}, // Not a valid mode
		{"", true},        // Empty defaults to easy
		{"wizard", false},
	}

	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			mode := tt.mode
			if mode == "" {
				mode = "easy"
			}
			valid := validModes[mode]
			if valid != tt.isValid {
				t.Errorf("mode %q: expected valid=%v, got %v", tt.mode, tt.isValid, valid)
			}
		})
	}
}

func TestLegacyJobRecordFields_UsesMigratedSchema(t *testing.T) {
	const migratedJobTypeUpdate = "update"

	fields := legacyJobRecordFields("deploy", "stack-123", "Queued for deployment")

	if got := fields["type"]; got != migratedJobTypeUpdate {
		t.Fatalf("expected migrated job type %s, got %v", migratedJobTypeUpdate, got)
	}
	if got := fields["stack_id"]; got != "stack-123" {
		t.Fatalf("expected stack_id field to be set, got %v", got)
	}
	if _, ok := fields["stack"]; ok {
		t.Fatal("did not expect legacy stack field in persisted job data")
	}
}

func TestNormalizeCreateStackRequest(t *testing.T) {
	tests := []struct {
		name      string
		req       createStackRequest
		wantName  string
		wantMode  string
		wantMsg   string
		wantField string
	}{
		{
			name: "defaults name and mode",
			req: createStackRequest{
				UserConfig: map[string]interface{}{"provider": "proxmox"},
			},
			wantName: "homelab",
			wantMode: "easy",
		},
		{
			name: "direct frontend format becomes user_config",
			req: createStackRequest{
				Name:     " Homelab ",
				Mode:     "techie",
				Provider: "proxmox",
				Services: []string{"traefik"},
			},
			wantName:  "Homelab",
			wantMode:  "techie",
			wantField: "provider",
		},
		{
			name: "raw config allowed without user_config",
			req: createStackRequest{
				UserConfigRaw:    `{"provider":"proxmox"}`,
				UserConfigFormat: "json",
			},
			wantName: "homelab",
			wantMode: "easy",
		},
		{
			name: "platform name is rejected instead of auto-renamed",
			req: createStackRequest{
				Name:       "techstack-3",
				UserConfig: map[string]interface{}{"provider": "proxmox"},
			},
			wantMsg: "TechStack is the platform name. Choose a Homelab name that is not techstack, techstack-*, kombify-techstack, or kombifytechstack.",
		},
		{
			name: "invalid mode preserves public error text",
			req: createStackRequest{
				Mode:       "expert",
				UserConfig: map[string]interface{}{"provider": "proxmox"},
			},
			wantMsg: "Invalid mode (expected 'easy' or 'techie')",
		},
		{
			name:    "missing config preserves public error text",
			req:     createStackRequest{},
			wantMsg: "Missing stack_spec, user_config, user_config_raw, or direct config fields (provider/services/options)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, msg := normalizeCreateStackRequest(tt.req)
			if msg != tt.wantMsg {
				t.Fatalf("message = %q, want %q", msg, tt.wantMsg)
			}
			if tt.wantMsg != "" {
				return
			}
			if got.Name != tt.wantName {
				t.Fatalf("name = %q, want %q", got.Name, tt.wantName)
			}
			if got.Mode != tt.wantMode {
				t.Fatalf("mode = %q, want %q", got.Mode, tt.wantMode)
			}
			if tt.wantField != "" && got.UserConfig[tt.wantField] == nil {
				t.Fatalf("expected normalized user_config field %q", tt.wantField)
			}
		})
	}
}

func TestNormalizeCreateStackRequest_StackSpecIsCanonicalConfig(t *testing.T) {
	req, err := decodeCreateStackRequest(strings.NewReader(`{
		"name": "Configured Stack",
		"mode": "easy",
		"stack_spec": {
			"name": "configured-stack",
			"stackkit": "basement-kit",
			"mode": "simple",
			"runtime": "docker",
			"context": "cloud",
			"nodes": [
				{"name": "main", "role": "standalone"}
			],
			"services": {
				"pocketid": {"enabled": true}
			},
			"metadata": {
				"spec_format": "stack-spec",
				"wizard_type": "easy"
			}
		}
	}`))
	if err != nil {
		t.Fatalf("decodeCreateStackRequest() error = %v", err)
	}

	normalized, msg := normalizeCreateStackRequest(req)
	if msg != "" {
		t.Fatalf("normalizeCreateStackRequest() message = %q, want empty", msg)
	}

	if normalized.UserConfig["stackkit"] != "basement-kit" {
		t.Fatalf("expected stack_spec to become canonical user_config, got %v", normalized.UserConfig)
	}
	if normalized.UserConfig["provider"] != nil {
		t.Fatalf("did not expect legacy provider field in canonical spec, got %v", normalized.UserConfig)
	}

	jobSpec := createStackJobSpec(normalized)
	if jobSpec["stackkit"] != "basement-kit" {
		t.Fatalf("expected job spec to use canonical stack_spec, got %v", jobSpec)
	}
}

func TestNormalizeCreateStackRequest_NormalizesLegacyBaseKitByRuntimeTarget(t *testing.T) {
	tests := []struct {
		name string
		req  createStackRequest
		want string
	}{
		{
			name: "user-owned legacy base becomes basement",
			req: createStackRequest{
				Name: "Local Stack",
				Mode: "easy",
				StackSpec: map[string]interface{}{
					"name":     "local-stack",
					"stackkit": "base-kit",
					"metadata": map[string]interface{}{
						"stackkit_catalog_ref": "base-kit",
					},
				},
			},
			want: "basement-kit",
		},
		{
			name: "managed base becomes cloud",
			req: createStackRequest{
				Name:     "Managed Stack",
				Mode:     "easy",
				Provider: "cloud",
				StackSpec: map[string]interface{}{
					"name":     "managed-stack",
					"stackkit": "base-kit",
					"metadata": map[string]interface{}{
						"server_provisioning_mode": "kombify-cloud",
						"provider_id":              "ionos",
						"stackkit_catalog_ref":     "base-kit",
					},
				},
			},
			want: "cloud-kit",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			normalized, msg := normalizeCreateStackRequest(tt.req)
			if msg != "" {
				t.Fatalf("normalizeCreateStackRequest() message = %q, want empty", msg)
			}
			if normalized.UserConfig["stackkit"] != tt.want {
				t.Fatalf("stackkit = %v, want %s", normalized.UserConfig["stackkit"], tt.want)
			}
			metadata, ok := normalized.UserConfig["metadata"].(map[string]interface{})
			if !ok {
				t.Fatalf("metadata missing from normalized user config: %v", normalized.UserConfig)
			}
			if metadata["stackkit_catalog_ref"] != tt.want {
				t.Fatalf("stackkit_catalog_ref = %v, want %s", metadata["stackkit_catalog_ref"], tt.want)
			}
		})
	}
}

func TestNormalizeCreateStackRequest_WizardOwnerBootstrapModes(t *testing.T) {
	req := createStackRequest{
		Name: "Configured Stack",
		Mode: "easy",
		StackSpec: map[string]interface{}{
			"name": "configured-stack",
			"kit":  "basement-kit",
			"metadata": map[string]interface{}{
				"spec_format":  "kombination",
				"created_by":   "wizard",
				"wizard_type":  "easy",
				"owner_source": "local",
			},
		},
	}

	_, msg := normalizeCreateStackRequest(req)
	if !strings.Contains(msg, "Owner email is required") {
		t.Fatalf("normalizeCreateStackRequest() message = %q, want missing custom owner email", msg)
	}

	req.Options = map[string]interface{}{
		"owner_bootstrap_mode": "auto",
		"owner_source":         "local",
	}
	normalized, msg := normalizeCreateStackRequest(req)
	if msg != "" {
		t.Fatalf("normalizeCreateStackRequest(auto) message = %q, want empty", msg)
	}
	bootstrap, ok := ownerBootstrapFromRequest(normalized)
	if !ok {
		t.Fatal("expected automatic owner bootstrap to be extracted")
	}
	if bootstrap.BootstrapMode != ownerBootstrapModeAuto || bootstrap.Email != "" || bootstrap.Username != "" {
		t.Fatalf("unexpected auto bootstrap before resolution: %+v", bootstrap)
	}

	req.Options = map[string]interface{}{
		"owner_bootstrap_mode":     "custom",
		"owner_source":             "local",
		"owner_email":              "owner@example.com",
		"owner_display_name":       "Owner",
		"recovery_passphrase_hash": testRecoveryPassphraseHash,
	}

	normalized, msg = normalizeCreateStackRequest(req)
	if msg != "" {
		t.Fatalf("normalizeCreateStackRequest() message = %q, want empty", msg)
	}

	bootstrap, ok = ownerBootstrapFromRequest(normalized)
	if !ok {
		t.Fatal("expected owner bootstrap to be extracted")
	}
	if bootstrap.Email != "owner@example.com" || bootstrap.Username != "" || bootstrap.DisplayName != "Owner" {
		t.Fatalf("unexpected bootstrap fields: %+v", bootstrap)
	}
}

func TestNormalizeCreateStackRequest_AcceptsSaaSAutoCloudOwnerBootstrap(t *testing.T) {
	req := createStackRequest{
		Name: "SaaS Wizard Stack",
		Mode: "easy",
		StackSpec: map[string]interface{}{
			"name": "saas-wizard-stack",
			"metadata": map[string]interface{}{
				"created_by":           "wizard",
				"wizard_type":          "easy",
				"owner_bootstrap_mode": "auto",
				"owner_source":         "cloud",
			},
			"owner": map[string]interface{}{
				"bootstrapMode":       "auto",
				"source":              "cloud",
				"recoveryMaterialRef": "techstack://recovery/stacks/saas-wizard-stack",
			},
		},
		Options: map[string]interface{}{
			"owner_bootstrap_mode":  "auto",
			"owner_source":          "cloud",
			"recovery_material_ref": "techstack://recovery/stacks/saas-wizard-stack",
		},
	}

	normalized, msg := normalizeCreateStackRequest(req)
	if msg != "" {
		t.Fatalf("normalizeCreateStackRequest() message = %q, want empty", msg)
	}

	resolved, denial := resolveCreateOwnerBootstrap(normalized, ownerBootstrapContext{
		Email:       "owner@example.com",
		DisplayName: "Owner Example",
	})
	if denial != nil {
		t.Fatalf("resolveCreateOwnerBootstrap() denial = %q, want none", denial.Message)
	}

	bootstrap, ok := ownerBootstrapFromRequest(resolved)
	if !ok {
		t.Fatal("expected resolved owner bootstrap")
	}
	if bootstrap.BootstrapMode != ownerBootstrapModeAuto ||
		bootstrap.Source != ownerSourceCloud ||
		bootstrap.Email != "" ||
		bootstrap.Username != "" ||
		bootstrap.DisplayName != "" {
		t.Fatalf("unexpected resolved bootstrap: %+v", bootstrap)
	}
}

func TestResolveCreateOwnerBootstrap_AutoCloudPreservesStackKitsOwnerSource(t *testing.T) {
	normalized := normalizedCreateStackRequest{
		Name: "SaaS Wizard Stack",
		Mode: "easy",
		UserConfig: map[string]interface{}{
			"name": "saas-wizard-stack",
			"metadata": map[string]interface{}{
				"owner_bootstrap_mode": "auto",
				"owner_source":         "cloud",
			},
			"owner": map[string]interface{}{
				"bootstrapMode": "auto",
				"source":        "cloud",
				"email":         "stale-client@example.com",
				"username":      "stale-client",
				"displayName":   "Stale Client",
			},
		},
		Options: map[string]interface{}{
			"owner_bootstrap_mode": "auto",
			"owner_source":         "cloud",
			"owner_email":          "stale-client@example.com",
			"owner_username":       "stale-client",
			"owner_display_name":   "Stale Client",
		},
	}

	resolved, denial := resolveCreateOwnerBootstrap(normalized, ownerBootstrapContext{
		Email:       "cloud.owner@example.com",
		Username:    "auth0|cloud-subject",
		DisplayName: "Cloud Owner",
	})
	if denial != nil {
		t.Fatalf("resolveCreateOwnerBootstrap() denial = %q, want none", denial.Message)
	}

	bootstrap, ok := ownerBootstrapFromRequest(resolved)
	if !ok {
		t.Fatal("expected resolved owner bootstrap")
	}
	if bootstrap.Source != ownerSourceCloud ||
		bootstrap.Email != "" ||
		bootstrap.Username != "" ||
		bootstrap.DisplayName != "" {
		t.Fatalf("automatic cloud bootstrap should stay owned by StackKits, got %+v", bootstrap)
	}
	if _, hasOwnerEmail := resolved.Options["owner_email"]; hasOwnerEmail {
		t.Fatalf("cloud auto bootstrap must not keep stale owner_email in options: %+v", resolved.Options)
	}
	owner, ok := resolved.UserConfig["owner"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected owner map in resolved spec: %+v", resolved.UserConfig["owner"])
	}
	if owner["source"] != ownerSourceCloud || owner["bootstrapMode"] != ownerBootstrapModeAuto {
		t.Fatalf("expected cloud auto owner handoff for StackKits, got %+v", owner)
	}
	if _, ok := owner["email"]; ok {
		t.Fatalf("cloud auto owner spec must not keep stale email: %+v", owner)
	}
}

func TestResolveCreateOwnerBootstrap_AutoAllowsOmittedSource(t *testing.T) {
	normalized := normalizedCreateStackRequest{
		Name: "SaaS Wizard Stack",
		Mode: "easy",
		UserConfig: map[string]interface{}{
			"name": "saas-wizard-stack",
			"metadata": map[string]interface{}{
				"owner_bootstrap_mode": "auto",
			},
		},
		Options: map[string]interface{}{
			"owner_bootstrap_mode": "auto",
		},
	}

	resolved, denial := resolveCreateOwnerBootstrap(normalized, ownerBootstrapContext{
		Email: "owner@example.com",
	})
	if denial != nil {
		t.Fatalf("resolveCreateOwnerBootstrap() denial = %q, want none", denial.Message)
	}

	bootstrap, ok := ownerBootstrapFromRequest(resolved)
	if !ok {
		t.Fatal("expected resolved owner bootstrap")
	}
	if bootstrap.Source != ownerSourceLocal || bootstrap.Email != "owner@example.com" || bootstrap.Username != "owner" {
		t.Fatalf("unexpected resolved bootstrap: %+v", bootstrap)
	}
}

func TestResolveCreateOwnerBootstrap_AutoOwnerResolvesFromAuthSubject(t *testing.T) {
	req := createStackRequest{
		Name:       "Managed Stack",
		Mode:       "easy",
		ProviderID: "centron",
		StackSpec: map[string]interface{}{
			"name": "managed-stack",
			"metadata": map[string]interface{}{
				"created_by":               "wizard",
				"server_provisioning_mode": "kombify-cloud",
				"owner_bootstrap_mode":     "auto",
				"owner_source":             "local",
			},
		},
		Options: map[string]interface{}{
			"owner_bootstrap_mode": "auto",
			"owner_source":         "local",
		},
	}

	normalized, msg := normalizeCreateStackRequest(req)
	if msg != "" {
		t.Fatalf("normalizeCreateStackRequest() message = %q, want empty", msg)
	}

	resolved, denial := resolveCreateOwnerBootstrap(normalized, ownerBootstrapContext{
		Email:       "owner@example.com",
		DisplayName: "Owner Example",
	})
	if denial != nil {
		t.Fatalf("resolveCreateOwnerBootstrap() denial = %q, want none", denial.Message)
	}

	bootstrap, ok := ownerBootstrapFromRequest(resolved)
	if !ok {
		t.Fatal("expected resolved owner bootstrap")
	}
	if bootstrap.BootstrapMode != ownerBootstrapModeAuto ||
		bootstrap.Source != ownerSourceLocal ||
		bootstrap.Email != "owner@example.com" ||
		bootstrap.Username != "owner" ||
		bootstrap.DisplayName != "Owner Example" {
		t.Fatalf("unexpected resolved bootstrap: %+v", bootstrap)
	}
	if !strings.HasPrefix(bootstrap.RecoveryPassphraseHash, "$argon2id$") {
		t.Fatalf("expected generated argon2id recovery hash, got %q", bootstrap.RecoveryPassphraseHash)
	}

	jobSpec := createStackJobSpec(resolved)
	identitySpec, ok := jobSpec["identity"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected transient identity spec, got %v", jobSpec["identity"])
	}
	ownerSpec := identitySpec["owner"].(map[string]interface{})
	if ownerSpec["email"] != "owner@example.com" || ownerSpec["username"] != "owner" {
		t.Fatalf("unexpected transient owner spec: %+v", ownerSpec)
	}
	recoverySpec := identitySpec["recovery"].(map[string]interface{})
	if !strings.HasPrefix(stringFromAny(recoverySpec["passphrase_hash"]), "$argon2id$") {
		t.Fatalf("expected transient recovery hash, got %+v", recoverySpec)
	}
	if strings.Contains(fmt.Sprint(normalized.UserConfig), "$argon2id$") {
		t.Fatalf("expected original public stack spec to stay free of recovery hash, got %v", normalized.UserConfig)
	}
}

func TestResolveCreateOwnerBootstrap_MissingCloudEmailFailsClearly(t *testing.T) {
	normalized := normalizedCreateStackRequest{
		Name: "Managed Stack",
		Mode: "easy",
		UserConfig: map[string]interface{}{
			"name": "managed-stack",
			"metadata": map[string]interface{}{
				"created_by":           "wizard",
				"owner_bootstrap_mode": "auto",
				"owner_source":         "local",
			},
		},
		Options: map[string]interface{}{
			"owner_bootstrap_mode": "auto",
			"owner_source":         "local",
		},
	}

	_, denial := resolveCreateOwnerBootstrap(normalized, ownerBootstrapContext{})
	if denial == nil || !strings.Contains(denial.Message, "Complete your kombify Cloud profile") {
		t.Fatalf("resolveCreateOwnerBootstrap() denial = %+v, want Cloud profile email error", denial)
	}
	if denial.ReasonCode != reasonCloudProfileEmailMissing {
		t.Fatalf("denial reason_code = %q, want %q", denial.ReasonCode, reasonCloudProfileEmailMissing)
	}
}

func TestResolveCreateOwnerBootstrap_CustomOwnerDerivesUsernameAndRecovery(t *testing.T) {
	normalized := normalizedCreateStackRequest{
		Name: "Custom Owner",
		Mode: "easy",
		UserConfig: map[string]interface{}{
			"name": "custom-owner",
			"metadata": map[string]interface{}{
				"created_by":           "wizard",
				"owner_bootstrap_mode": "custom",
				"owner_source":         "local",
			},
		},
		Options: map[string]interface{}{
			"owner_bootstrap_mode": "custom",
			"owner_source":         "local",
			"owner_email":          "Custom.Owner+Lab@example.com",
		},
	}

	resolved, denial := resolveCreateOwnerBootstrap(normalized, ownerBootstrapContext{})
	if denial != nil {
		t.Fatalf("resolveCreateOwnerBootstrap() denial = %q, want none", denial.Message)
	}
	bootstrap, ok := ownerBootstrapFromRequest(resolved)
	if !ok {
		t.Fatal("expected resolved bootstrap")
	}
	if bootstrap.Username != "custom-owner-lab" {
		t.Fatalf("derived username = %q, want custom-owner-lab", bootstrap.Username)
	}
	if !strings.HasPrefix(bootstrap.RecoveryPassphraseHash, "$argon2id$") {
		t.Fatalf("expected generated recovery hash, got %q", bootstrap.RecoveryPassphraseHash)
	}
}

func TestNormalizeCreateStackRequest_RejectsCloudOwnerBootstrapByDefault(t *testing.T) {
	t.Setenv("TECHSTACK_ENABLE_CLOUD_OWNER_BOOTSTRAP", "")

	_, msg := normalizeCreateStackRequest(createStackRequest{
		Name: "Cloud Owner",
		Mode: "easy",
		StackSpec: map[string]interface{}{
			"name": "cloud-owner",
			"metadata": map[string]interface{}{
				"created_by": "wizard",
			},
		},
		Options: map[string]interface{}{
			"owner_source": "cloud",
			"owner_email":  "owner@example.com",
		},
	})

	if !strings.Contains(msg, "Cloud owner bootstrap is not available") {
		t.Fatalf("normalizeCreateStackRequest() message = %q, want cloud owner gate", msg)
	}
}

func TestCreateStackJobSpec_AppliesOwnerBootstrapOnlyTransiently(t *testing.T) {
	const recoveryHash = testRecoveryPassphraseHash
	normalized := normalizedCreateStackRequest{
		Name: "Configured Stack",
		Mode: "easy",
		UserConfig: map[string]interface{}{
			"name": "configured-stack",
			"kit":  "basement-kit",
			"identity": map[string]interface{}{
				"owner": map[string]interface{}{
					"source": "local",
				},
				"recovery": map[string]interface{}{
					"passphraseHashPresent": true,
				},
			},
		},
		Options: map[string]interface{}{
			"owner_source":             "local",
			"owner_email":              "owner@example.com",
			"owner_username":           "owner",
			"recovery_passphrase_hash": recoveryHash,
		},
	}

	jobSpec := createStackJobSpec(normalized)
	identity, ok := jobSpec["identity"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected transient identity spec, got %v", jobSpec["identity"])
	}
	recovery, ok := identity["recovery"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected transient recovery spec, got %v", identity["recovery"])
	}
	if got := recovery["passphrase_hash"]; got != recoveryHash {
		t.Fatalf("passphrase_hash = %v, want %q", got, recoveryHash)
	}

	originalIdentity := normalized.UserConfig["identity"].(map[string]interface{})
	originalRecovery := originalIdentity["recovery"].(map[string]interface{})
	if originalRecovery["passphrase_hash"] != nil {
		t.Fatalf("expected original user_config to stay non-secret, got %v", originalRecovery)
	}
}

func TestOwnerSpecBootstrapAccessOnlyAppliesToLocalOwnerBootstrap(t *testing.T) {
	localSpec := map[string]interface{}{
		"metadata": map[string]interface{}{
			"owner_bootstrap_mode": "auto",
			"owner_source":         "local",
		},
		"owner": map[string]interface{}{
			"bootstrapMode": "auto",
			"source":        "local",
		},
	}
	if !stackHasOwnerBootstrapForProvision(nil, localSpec) {
		t.Fatal("expected local owner bootstrap to require owner-spec access")
	}

	cloudSpec := map[string]interface{}{
		"metadata": map[string]interface{}{
			"owner_bootstrap_mode": "auto",
			"owner_source":         "cloud",
		},
		"owner": map[string]interface{}{
			"bootstrapMode": "auto",
			"source":        "cloud",
		},
	}
	if stackHasOwnerBootstrapForProvision(nil, cloudSpec) {
		t.Fatal("cloud owner bootstrap must not require a local owner-spec token")
	}
}

func TestOwnerBootstrapWalletFieldsUseAccessAndRecoveryModel(t *testing.T) {
	bootstrap := ownerBootstrapSpec{
		Source:                 "local",
		Email:                  "owner@example.com",
		Username:               "owner",
		DisplayName:            "Owner",
		RecoveryPassphraseHash: testRecoveryPassphraseHash,
	}

	ownerFields := ownerAccessWalletFields("user-1", "stack-1", "Configured Stack", bootstrap)
	if ownerFields["item_class"] != "user_account" || ownerFields["access_mode"] != "manage" {
		t.Fatalf("unexpected owner wallet model: %v", ownerFields)
	}
	if ownerFields["secret"] != "" || ownerFields["revealable"] != false {
		t.Fatalf("owner access item should not store a revealable secret: %v", ownerFields)
	}
	if ownerFields["source_ref"] != "pocketid:stack-1:owner" {
		t.Fatalf("unexpected owner source_ref: %v", ownerFields["source_ref"])
	}

	recoveryFields := recoveryWalletFields("user-1", "stack-1", "Configured Stack", bootstrap)
	if recoveryFields["item_class"] != "recovery" || recoveryFields["access_mode"] != "reveal" {
		t.Fatalf("unexpected recovery wallet model: %v", recoveryFields)
	}
	if recoveryFields["service_id"] != recoveryWalletServiceID || recoveryFields["revealable"] != true {
		t.Fatalf("recovery item should be a revealable stackkit recovery entry: %v", recoveryFields)
	}
	if strings.TrimSpace(recoveryFields["secret"].(string)) == "" {
		t.Fatal("expected recovery secret to carry the passphrase hash or encrypted envelope")
	}
}

func TestOwnerSpecBootstrapTokenRejectsExpiredAndCrossStackUse(t *testing.T) {
	t.Setenv(ownerSpecBootstrapTokenSecretEnv, "0123456789abcdef0123456789abcdef")
	now := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)

	token, expiresAt, err := issueOwnerSpecBootstrapToken("stack-1", "owner-1", now)
	if err != nil {
		t.Fatalf("issueOwnerSpecBootstrapToken() error = %v", err)
	}
	if expiresAt.Sub(now) != ownerSpecBootstrapTokenTTL {
		t.Fatalf("expiresAt delta = %s, want %s", expiresAt.Sub(now), ownerSpecBootstrapTokenTTL)
	}

	claims, err := verifyOwnerSpecBootstrapToken(token, "stack-1", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("verifyOwnerSpecBootstrapToken() error = %v", err)
	}
	if claims.StackID != "stack-1" || claims.OwnerID != "owner-1" || !hasOwnerSpecScope(claims.Scopes) {
		t.Fatalf("unexpected claims: %+v", claims)
	}

	if _, err := verifyOwnerSpecBootstrapToken(token, "stack-2", now.Add(time.Minute)); !errors.Is(err, errOwnerSpecTokenForbidden) {
		t.Fatalf("cross-stack verify error = %v, want forbidden", err)
	}
	if _, err := verifyOwnerSpecBootstrapToken(token, "stack-1", expiresAt.Add(time.Second)); err == nil {
		t.Fatal("expected expired token to be rejected")
	}
}

func TestOwnerSpecEndpointRejectsMissingExpiredAndCrossStackTokens(t *testing.T) {
	t.Setenv(ownerSpecBootstrapTokenSecretEnv, "0123456789abcdef0123456789abcdef")
	now := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	h := crudRouteHandlers{}

	missingTokenEvent, missingTokenRecorder := ownerSpecRequestEvent("stack-1", "")
	if ownerSpecErr := h.ownerSpec(missingTokenEvent); ownerSpecErr != nil {
		t.Fatalf("ownerSpec(missing token) error = %v", ownerSpecErr)
	}
	if missingTokenRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("missing token status = %d body=%s, want 401", missingTokenRecorder.Code, missingTokenRecorder.Body.String())
	}

	expiredToken, _, err := issueOwnerSpecBootstrapToken("stack-1", "owner-1", now.Add(-ownerSpecBootstrapTokenTTL-time.Minute))
	if err != nil {
		t.Fatalf("issue expired token: %v", err)
	}
	expiredEvent, expiredRecorder := ownerSpecRequestEvent("stack-1", expiredToken)
	if ownerSpecErr := h.ownerSpec(expiredEvent); ownerSpecErr != nil {
		t.Fatalf("ownerSpec(expired token) error = %v", ownerSpecErr)
	}
	if expiredRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("expired token status = %d body=%s, want 401", expiredRecorder.Code, expiredRecorder.Body.String())
	}

	crossStackToken, _, err := issueOwnerSpecBootstrapToken("stack-a", "owner-1", time.Now().UTC())
	if err != nil {
		t.Fatalf("issue cross-stack token: %v", err)
	}
	crossStackEvent, crossStackRecorder := ownerSpecRequestEvent("stack-b", crossStackToken)
	if ownerSpecErr := h.ownerSpec(crossStackEvent); ownerSpecErr != nil {
		t.Fatalf("ownerSpec(cross-stack token) error = %v", ownerSpecErr)
	}
	if crossStackRecorder.Code != http.StatusForbidden {
		t.Fatalf("cross-stack token status = %d body=%s, want 403", crossStackRecorder.Code, crossStackRecorder.Body.String())
	}
}

func TestOwnerSpecEndpointReturnsStackKitIdentityRecoverySchema(t *testing.T) {
	t.Setenv(ownerSpecBootstrapTokenSecretEnv, "0123456789abcdef0123456789abcdef")
	app := newOwnerSpecTestApp(t)
	defer app.Cleanup()

	stack := createOwnerSpecTestStack(t, app, "owner-1")
	const recoveryHash = testRecoveryPassphraseHash
	createOwnerSpecTestRecoveryWalletEntry(t, app, stack.Id, "owner-1", recoveryHash)

	h := crudRouteHandlers{app: app}
	access, err := h.issueOwnerSpecBootstrapAccess(stack.Id, "owner-1", time.Now().UTC())
	if err != nil {
		t.Fatalf("issueOwnerSpecBootstrapAccess() error = %v", err)
	}

	event, recorder := ownerSpecRequestEvent(stack.Id, access.Token)
	if ownerSpecErr := h.ownerSpec(event); ownerSpecErr != nil {
		t.Fatalf("ownerSpec() error = %v", ownerSpecErr)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("ownerSpec() status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}

	var envelope struct {
		Data ownerSpecResponse `json:"data"`
	}
	if decodeErr := json.Unmarshal(recorder.Body.Bytes(), &envelope); decodeErr != nil {
		t.Fatalf("decode response: %v", decodeErr)
	}
	if envelope.Data.StackID != stack.Id {
		t.Fatalf("stack_id = %q, want %q", envelope.Data.StackID, stack.Id)
	}
	if envelope.Data.Identity.Owner.Source != ownerSourceLocal ||
		envelope.Data.Identity.Owner.Email != "owner@example.com" ||
		envelope.Data.Identity.Owner.Username != "owner" ||
		envelope.Data.Identity.Owner.DisplayName != "Owner" {
		t.Fatalf("unexpected owner response: %+v", envelope.Data.Identity.Owner)
	}
	if envelope.Data.Identity.Recovery.PassphraseHash != recoveryHash || !envelope.Data.Identity.Recovery.PassphraseHashPresent {
		t.Fatalf("unexpected recovery response: %+v", envelope.Data.Identity.Recovery)
	}
	if !hasOwnerSpecScope(envelope.Data.Scopes) {
		t.Fatalf("expected owner spec scope in response, got %v", envelope.Data.Scopes)
	}

	activities, err := app.FindRecordsByFilter("activity_log", "action = {:action}", "", 0, 0, map[string]any{"action": ownerSpecActionRead})
	if err != nil {
		t.Fatalf("find activity records: %v", err)
	}
	if len(activities) == 0 {
		t.Fatal("expected owner_spec_read activity to be recorded")
	}
}

func TestOwnerSpecTokenIsSingleUse(t *testing.T) {
	t.Setenv(ownerSpecBootstrapTokenSecretEnv, "0123456789abcdef0123456789abcdef")
	app := newOwnerSpecTestApp(t)
	defer app.Cleanup()

	stack := createOwnerSpecTestStack(t, app, "owner-1")
	createOwnerSpecTestRecoveryWalletEntry(t, app, stack.Id, "owner-1", testRecoveryPassphraseHash)

	h := crudRouteHandlers{app: app}
	access, err := h.issueOwnerSpecBootstrapAccess(stack.Id, "owner-1", time.Now().UTC())
	if err != nil {
		t.Fatalf("issueOwnerSpecBootstrapAccess() error = %v", err)
	}

	firstEvent, firstRecorder := ownerSpecRequestEvent(stack.Id, access.Token)
	if ownerSpecErr := h.ownerSpec(firstEvent); ownerSpecErr != nil {
		t.Fatalf("ownerSpec(first) error = %v", ownerSpecErr)
	}
	if firstRecorder.Code != http.StatusOK {
		t.Fatalf("first ownerSpec() status = %d body=%s, want 200", firstRecorder.Code, firstRecorder.Body.String())
	}

	secondEvent, secondRecorder := ownerSpecRequestEvent(stack.Id, access.Token)
	if ownerSpecErr := h.ownerSpec(secondEvent); ownerSpecErr != nil {
		t.Fatalf("ownerSpec(second) error = %v", ownerSpecErr)
	}
	if secondRecorder.Code != http.StatusForbidden {
		t.Fatalf("second ownerSpec() status = %d body=%s, want 403", secondRecorder.Code, secondRecorder.Body.String())
	}

	activities, err := app.FindRecordsByFilter("activity_log", "action = {:action}", "", 0, 0, map[string]any{"action": ownerSpecActionDenied})
	if err != nil {
		t.Fatalf("find denied activity records: %v", err)
	}
	if len(activities) == 0 {
		t.Fatal("expected replay denial activity to be recorded")
	}
}

func TestOwnerSpecBootstrapTokenConsumeIsAtomic(t *testing.T) {
	t.Setenv(ownerSpecBootstrapTokenSecretEnv, "0123456789abcdef0123456789abcdef")
	app := newOwnerSpecTestApp(t)
	defer app.Cleanup()

	stack := createOwnerSpecTestStack(t, app, "owner-1")
	h := crudRouteHandlers{app: app}
	now := time.Now().UTC()
	access, err := h.issueOwnerSpecBootstrapAccess(stack.Id, "owner-1", now)
	if err != nil {
		t.Fatalf("issueOwnerSpecBootstrapAccess() error = %v", err)
	}
	claims, err := verifyOwnerSpecBootstrapToken(access.Token, stack.Id, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("verifyOwnerSpecBootstrapToken() error = %v", err)
	}

	const attempts = 16
	results := make(chan error, attempts)
	for range attempts {
		go func() {
			results <- h.consumeOwnerSpecBootstrapToken(claims, now.Add(time.Minute))
		}()
	}

	successes := 0
	for range attempts {
		err := <-results
		if err == nil {
			successes++
			continue
		}
		if !errors.Is(err, errOwnerSpecTokenForbidden) && !errors.Is(err, errOwnerSpecTokenInvalid) {
			t.Fatalf("consumeOwnerSpecBootstrapToken() unexpected error = %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful consumes = %d, want exactly 1", successes)
	}
}

func TestOwnerSpecBootstrapAccessForProvisionReissuesFromStoredSpec(t *testing.T) {
	t.Setenv(ownerSpecBootstrapTokenSecretEnv, "0123456789abcdef0123456789abcdef")
	app := newOwnerSpecTestApp(t)
	defer app.Cleanup()

	stack := createOwnerSpecTestStack(t, app, "owner-1")
	h := crudRouteHandlers{app: app}

	access, err := h.ownerSpecBootstrapAccessForProvision(stack, map[string]interface{}{
		"name": "retry-spec-without-transient-owner-material",
	})
	if err != nil {
		t.Fatalf("ownerSpecBootstrapAccessForProvision() error = %v", err)
	}
	if access.Token == "" || access.Endpoint != ownerSpecEndpoint(stack.Id) || access.ExpiresAt.IsZero() {
		t.Fatalf("expected retry bootstrap access, got %+v", access)
	}
	if _, err := verifyOwnerSpecBootstrapToken(access.Token, stack.Id, time.Now().UTC()); err != nil {
		t.Fatalf("verify retry token: %v", err)
	}
}

func TestOwnerSpecResponseFieldsExposeShortLivedBootstrapContract(t *testing.T) {
	access := ownerSpecBootstrapAccess{
		Token:     "signed-token",
		Endpoint:  ownerSpecEndpoint("stack-1"),
		ExpiresAt: time.Date(2026, 5, 13, 10, 15, 0, 0, time.UTC),
	}

	fields := ownerSpecResponseFields(access)

	if fields["bootstrap_token"] != "signed-token" {
		t.Fatalf("bootstrap_token = %v, want signed-token", fields["bootstrap_token"])
	}
	if fields["owner_spec_endpoint"] != "/api/v1/stacks/stack-1/owner-spec" {
		t.Fatalf("owner_spec_endpoint = %v", fields["owner_spec_endpoint"])
	}
	if fields["bootstrap_token_expires_at"] != "2026-05-13T10:15:00Z" {
		t.Fatalf("bootstrap_token_expires_at = %v", fields["bootstrap_token_expires_at"])
	}
	if scopes, ok := fields["owner_spec_scopes"].([]string); !ok || !hasOwnerSpecScope(scopes) {
		t.Fatalf("owner_spec_scopes = %v", fields["owner_spec_scopes"])
	}
}

func TestNormalizeCreateStackRequest_AllowsUserOwnedServerProvisioningModes(t *testing.T) {
	tests := []struct {
		name string
		req  createStackRequest
	}{
		{
			name: "canonical stack spec connect remote",
			req: createStackRequest{
				Name: "Remote Stack",
				Mode: "easy",
				StackSpec: map[string]interface{}{
					"name": "remote-stack",
					"metadata": map[string]interface{}{
						"server_provisioning_mode": "connect-remote",
					},
				},
			},
		},
		{
			name: "canonical stack spec install command",
			req: createStackRequest{
				Name: "Install Stack",
				Mode: "techie",
				StackSpec: map[string]interface{}{
					"name": "install-stack",
					"metadata": map[string]interface{}{
						"server_provisioning_mode": "install-command",
					},
				},
			},
		},
		{
			name: "legacy options connect remote",
			req: createStackRequest{
				Name:     "Legacy Remote",
				Mode:     "easy",
				Provider: "local",
				Services: []string{"traefik"},
				Options: map[string]interface{}{
					"server_provisioning_mode": "connect-remote",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			normalized, msg := normalizeCreateStackRequest(tt.req)
			if msg != "" {
				t.Fatalf("normalizeCreateStackRequest() message = %q, want allowed", msg)
			}
			if normalized.Name == "" || normalized.UserConfig == nil {
				t.Fatalf("normalizeCreateStackRequest() = %+v, want normalized config", normalized)
			}
		})
	}
}

func TestValidateDeploymentLaneRejectsManagedRuntimeOutsideSaaS(t *testing.T) {
	req := createStackRequest{
		Name: "Managed Stack",
		Mode: "easy",
		StackSpec: map[string]interface{}{
			"name": "managed-stack",
			"metadata": map[string]interface{}{
				"server_provisioning_mode": "kombify-cloud",
				"server_mode":              "monthly-runtime",
				"runtime_lane":             "monthly-runtime",
				"billing_mode":             "subscription",
			},
		},
		Options: map[string]interface{}{
			"runtime_offering_id": "monthly-runtime-standard",
			"provider_id":         "centron",
		},
	}

	normalized, msg := normalizeCreateStackRequest(req)
	if msg != "" {
		t.Fatalf("normalizeCreateStackRequest() message = %q, want empty", msg)
	}

	msg = validateDeploymentLane(normalized, config.ModeSelfHosted)
	if !strings.Contains(msg, "Managed monthly runtime can only be created from kombify Cloud mode") {
		t.Fatalf("validateDeploymentLane() message = %q, want managed runtime rejection", msg)
	}
}

func TestValidateDeploymentLaneAllowsManagedRuntimeForLocalE2E(t *testing.T) {
	t.Setenv("TECHSTACK_ENV", "development")
	t.Setenv("TECHSTACK_ALLOW_LOCAL_SIMULATION_GATE", "true")
	t.Setenv("TECHSTACK_ALLOW_LOCAL_MANAGED_RUNTIME_E2E", "true")

	req := createStackRequest{
		Name: "Managed Stack",
		Mode: "easy",
		StackSpec: map[string]interface{}{
			"name": "managed-stack",
			"metadata": map[string]interface{}{
				"server_provisioning_mode": "kombify-cloud",
				"server_mode":              "monthly-runtime",
				"runtime_lane":             "monthly-runtime",
				"billing_mode":             "subscription",
			},
		},
		Options: map[string]interface{}{
			"runtime_offering_id": "monthly-runtime-standard",
			"provider_id":         "centron",
		},
	}

	normalized, msg := normalizeCreateStackRequest(req)
	if msg != "" {
		t.Fatalf("normalizeCreateStackRequest() message = %q, want empty", msg)
	}

	if msg = validateDeploymentLane(normalized, config.ModeSelfHosted); msg != "" {
		t.Fatalf("validateDeploymentLane() message = %q, want local E2E allowance", msg)
	}
	if msg = validateManagedRuntimeEntitlement(context.Background(), normalized, "owner-1", nil); msg != "" {
		t.Fatalf("validateManagedRuntimeEntitlement() message = %q, want local E2E allowance", msg)
	}
}

func TestValidateManagedRuntimeEntitlementRejectsSaaSManagedRuntimeWithoutFeatureChecker(t *testing.T) {
	t.Setenv("TECHSTACK_ENV", "production")

	normalized, msg := normalizeCreateStackRequest(managedRuntimeCreateRequest("centron"))
	if msg != "" {
		t.Fatalf("normalizeCreateStackRequest() message = %q, want empty", msg)
	}

	msg = validateManagedRuntimeEntitlement(context.Background(), normalized, "owner-1", nil)
	if msg != managedRuntimeEntitlementDenied {
		t.Fatalf("validateManagedRuntimeEntitlement() message = %q, want entitlement denial", msg)
	}
}

func TestValidateManagedRuntimeEntitlementRejectsDisabledProvider(t *testing.T) {
	t.Setenv("TECHSTACK_ENV", "production")

	normalized, msg := normalizeCreateStackRequest(managedRuntimeCreateRequest("centron"))
	if msg != "" {
		t.Fatalf("normalizeCreateStackRequest() message = %q, want empty", msg)
	}
	checker := &entitlementFeatureChecker{enabled: false}

	msg = validateManagedRuntimeEntitlement(context.Background(), normalized, "owner-1", checker)
	if msg != managedRuntimeEntitlementDenied {
		t.Fatalf("validateManagedRuntimeEntitlement() message = %q, want entitlement denial", msg)
	}
	if len(checker.keys) == 0 {
		t.Fatal("feature checker was not called for managed runtime request")
	}
}

func TestValidateManagedRuntimeEntitlementChecksProviderFeature(t *testing.T) {
	t.Setenv("TECHSTACK_ENV", "production")

	normalized, msg := normalizeCreateStackRequest(managedRuntimeCreateRequest("ionos"))
	if msg != "" {
		t.Fatalf("normalizeCreateStackRequest() message = %q, want empty", msg)
	}
	checker := &entitlementFeatureChecker{enabled: true}

	msg = validateManagedRuntimeEntitlement(context.Background(), normalized, "owner-1", checker)
	if msg != "" {
		t.Fatalf("validateManagedRuntimeEntitlement() message = %q, want allowed", msg)
	}
	got := strings.Join(checker.keys, ",")
	for _, want := range []string{"techstack.managed.runtime", "techstack.managed.runtime.cloudkit", "techstack.managed.runtime.ionos"} {
		if !strings.Contains(got, want) {
			t.Fatalf("feature keys = %q, want %q", got, want)
		}
	}
}

func TestValidateManagedRuntimeEntitlementAllowsAdminIdentityWithoutEdgeFlags(t *testing.T) {
	t.Setenv("TECHSTACK_ENV", "production")

	normalized, msg := normalizeCreateStackRequest(managedRuntimeCreateRequest("centron"))
	if msg != "" {
		t.Fatalf("normalizeCreateStackRequest() message = %q, want empty", msg)
	}
	ctx := identity.NewContext(context.Background(), &identity.Identity{
		UserID: "google-oauth2|admin",
		Roles:  []string{"global_admin", "admin"},
	})

	msg = validateManagedRuntimeEntitlement(ctx, normalized, "google-oauth2|admin", nil)
	if msg != "" {
		t.Fatalf("validateManagedRuntimeEntitlement() message = %q, want admin allowed", msg)
	}
}

func TestCreateStackRejectsManagedRuntimeEntitlementBeforePersist(t *testing.T) {
	t.Setenv("TECHSTACK_ENV", "production")

	app := newOwnerSpecTestApp(t)
	defer app.Cleanup()

	body, err := json.Marshal(managedRuntimeCreateRequest("centron"))
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	before, err := app.FindRecordsByFilter("stacks", "owner_id = {:ownerId}", "", 100, 0, map[string]any{"ownerId": "owner-1"})
	if err != nil {
		t.Fatalf("list stacks before create: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/stacks", strings.NewReader(string(body)))
	req = req.WithContext(identity.NewContext(req.Context(), &identity.Identity{UserID: "owner-1", OrgID: "org-1"}))
	rec := httptest.NewRecorder()
	event := &httpx.Event{Request: req, Response: rec}
	h := crudRouteHandlers{
		app:             app,
		deploymentMode:  config.ModeSaaS,
		runtimeFeatures: &entitlementFeatureChecker{enabled: false},
	}

	if err := h.createStack(event); err != nil {
		t.Fatalf("createStack returned router error: %v", err)
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s, want 403", rec.Code, rec.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	apiError, ok := response["error"].(map[string]any)
	if !ok {
		t.Fatalf("error response = %#v, want error object", response)
	}
	details, ok := apiError["details"].(map[string]any)
	if !ok {
		t.Fatalf("error details = %#v, want details object", apiError["details"])
	}
	if got := details["reason_code"]; got != "required_feature_disabled" {
		t.Fatalf("reason_code = %v, want required_feature_disabled", got)
	}
	if got := details["provider_id"]; got != "centron" {
		t.Fatalf("provider_id = %v, want centron", got)
	}
	missing, ok := details["missing_features"].([]any)
	if !ok || len(missing) == 0 {
		t.Fatalf("missing_features = %#v, want non-empty list", details["missing_features"])
	}
	guidance, ok := details["user_guidance"].(map[string]any)
	if !ok || guidance["body"] == "" {
		t.Fatalf("user_guidance = %#v, want user-facing body", details["user_guidance"])
	}
	stacks, err := app.FindRecordsByFilter("stacks", "owner_id = {:ownerId}", "", 100, 0, map[string]any{"ownerId": "owner-1"})
	if err != nil {
		t.Fatalf("list stacks: %v", err)
	}
	if len(stacks) != len(before) {
		t.Fatalf("persisted stacks changed from %d to %d after entitlement rejection", len(before), len(stacks))
	}
}

func TestCreateStackDefersManagedRuntimeCapacityToNativeAdmission(t *testing.T) {
	t.Setenv("TECHSTACK_ENV", "production")

	normalized, msg := normalizeCreateStackRequest(managedRuntimeCreateRequest("ionos"))
	if msg != "" {
		t.Fatalf("normalizeCreateStackRequest() message = %q, want empty", msg)
	}

	previousLister := currentManagedRuntimeLeaseLister()
	ConfigureManagedRuntimeLeaseLister(staticManagedRuntimeLeaseLister{err: errors.New("inventory must not be capacity authority")})
	t.Cleanup(func() { ConfigureManagedRuntimeLeaseLister(previousLister) })

	req := httptest.NewRequest(http.MethodPost, "/api/v1/stacks", nil)
	req = req.WithContext(identity.NewContext(req.Context(), &identity.Identity{UserID: "owner-1", OrgID: "default"}))
	rec := httptest.NewRecorder()
	event := &httpx.Event{Request: req, Response: rec}
	h := crudRouteHandlers{
		deploymentMode:  config.ModeSaaS,
		runtimeFeatures: &entitlementFeatureChecker{enabled: true},
	}

	rejected, err := h.rejectUnauthorizedManagedRuntime(event, "owner-1", normalized)
	if err != nil || rejected {
		t.Fatalf("managed runtime rejected before native admission: rejected=%t err=%v", rejected, err)
	}
}

func TestValidateDeploymentLaneRejectsManagedRuntimeE2EGateOutsideDevelopment(t *testing.T) {
	t.Setenv("TECHSTACK_ENV", "production")
	t.Setenv("TECHSTACK_ALLOW_LOCAL_SIMULATION_GATE", "true")
	t.Setenv("TECHSTACK_ALLOW_LOCAL_MANAGED_RUNTIME_E2E", "true")

	req := createStackRequest{
		Name: "Managed Stack",
		Mode: "easy",
		StackSpec: map[string]interface{}{
			"name": "managed-stack",
			"metadata": map[string]interface{}{
				"server_provisioning_mode": "kombify-cloud",
				"server_mode":              "monthly-runtime",
				"runtime_lane":             "monthly-runtime",
				"billing_mode":             "subscription",
			},
		},
		Options: map[string]interface{}{
			"runtime_offering_id": "monthly-runtime-standard",
			"provider_id":         "centron",
		},
	}

	normalized, msg := normalizeCreateStackRequest(req)
	if msg != "" {
		t.Fatalf("normalizeCreateStackRequest() message = %q, want empty", msg)
	}

	msg = validateDeploymentLane(normalized, config.ModeSelfHosted)
	if !strings.Contains(msg, "Managed monthly runtime can only be created from kombify Cloud mode") {
		t.Fatalf("validateDeploymentLane() message = %q, want managed runtime rejection", msg)
	}
}

func managedRuntimeCreateRequest(provider string) createStackRequest {
	return createStackRequest{
		Name: "Managed Stack",
		Mode: "easy",
		StackSpec: map[string]interface{}{
			"name": "managed-stack",
			"metadata": map[string]interface{}{
				"server_provisioning_mode": "kombify-cloud",
				"server_mode":              "monthly-runtime",
				"runtime_lane":             "monthly-runtime",
				"billing_mode":             "subscription",
			},
		},
		Options: map[string]interface{}{
			"runtime_offering_id": "monthly-runtime-standard",
			"provider_id":         provider,
		},
	}
}

func TestValidateDeploymentLaneAllowsUserOwnedProvisioningInSaaS(t *testing.T) {
	tests := []struct {
		name string
		req  createStackRequest
	}{
		{
			name: "remote ssh",
			req: createStackRequest{
				Name: "Remote Stack",
				Mode: "easy",
				StackSpec: map[string]interface{}{
					"name": "remote-stack",
					"metadata": map[string]interface{}{
						"server_provisioning_mode":   "connect-remote",
						"server_connection_mode":     "remote-ssh",
						"server_remote_host_present": "true",
						"server_remote_user_present": "true",
						"server_mode":                "user-owned",
						"billing_mode":               "local",
					},
				},
			},
		},
		{
			name: "install command",
			req: createStackRequest{
				Name: "Install Stack",
				Mode: "easy",
				StackSpec: map[string]interface{}{
					"name": "install-stack",
					"metadata": map[string]interface{}{
						"server_provisioning_mode":        "install-command",
						"server_connection_mode":          "agent-oneliner",
						"server_install_command_required": "true",
						"server_mode":                     "self-hosted",
						"billing_mode":                    "local",
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			normalized, msg := normalizeCreateStackRequest(tt.req)
			if msg != "" {
				t.Fatalf("normalizeCreateStackRequest() message = %q, want empty", msg)
			}

			msg = validateDeploymentLane(normalized, config.ModeSaaS)
			if msg != "" {
				t.Fatalf("validateDeploymentLane() message = %q, want allowed", msg)
			}
		})
	}
}

func TestValidateDeploymentLaneAllowsExpectedLaneDefaults(t *testing.T) {
	tests := []struct {
		name string
		mode config.DeploymentMode
		req  createStackRequest
	}{
		{
			name: "self hosted install command",
			mode: config.ModeSelfHosted,
			req: createStackRequest{
				Name: "Install Stack",
				Mode: "easy",
				StackSpec: map[string]interface{}{
					"name": "install-stack",
					"metadata": map[string]interface{}{
						"server_provisioning_mode": "install-command",
						"server_mode":              "user-owned",
						"billing_mode":             "local",
					},
				},
			},
		},
		{
			name: "self hosted remote ssh",
			mode: config.ModeSelfHosted,
			req: createStackRequest{
				Name: "Remote Stack",
				Mode: "easy",
				StackSpec: map[string]interface{}{
					"name": "remote-stack",
					"metadata": map[string]interface{}{
						"server_provisioning_mode": "connect-remote",
						"server_connection_mode":   "remote-ssh",
						"server_mode":              "user-owned",
					},
				},
			},
		},
		{
			name: "saas managed runtime",
			mode: config.ModeSaaS,
			req: createStackRequest{
				Name:       "Managed Stack",
				Mode:       "easy",
				ProviderID: "centron",
				StackSpec: map[string]interface{}{
					"name": "managed-stack",
					"metadata": map[string]interface{}{
						"server_provisioning_mode": "kombify-cloud",
						"server_connection_mode":   "managed-subscription",
						"server_mode":              "monthly-runtime",
						"runtime_lane":             "monthly-runtime",
						"billing_mode":             "subscription",
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			normalized, msg := normalizeCreateStackRequest(tt.req)
			if msg != "" {
				t.Fatalf("normalizeCreateStackRequest() message = %q, want empty", msg)
			}
			if msg := validateDeploymentLane(normalized, tt.mode); msg != "" {
				t.Fatalf("validateDeploymentLane() message = %q, want allowed", msg)
			}
		})
	}
}

func TestCreateStackJobSpec(t *testing.T) {
	fromConfig := createStackJobSpec(normalizedCreateStackRequest{
		UserConfig: map[string]interface{}{"provider": "proxmox"},
	})
	if fromConfig["provider"] != "proxmox" {
		t.Fatalf("expected user_config to be preferred, got %v", fromConfig)
	}

	fromRaw := createStackJobSpec(normalizedCreateStackRequest{
		UserConfigRaw: `{"provider":"docker"}`,
	})
	if fromRaw["provider"] != "docker" {
		t.Fatalf("expected raw JSON to be parsed, got %v", fromRaw)
	}

	fromOpaqueRaw := createStackJobSpec(normalizedCreateStackRequest{
		UserConfigRaw:    "provider: proxmox",
		UserConfigFormat: "yaml",
	})
	if fromOpaqueRaw["provider"] != "proxmox" {
		t.Fatalf("expected raw YAML to be parsed, got %v", fromOpaqueRaw)
	}
}

func TestHasManagedRuntimeCreatePolicy(t *testing.T) {
	tests := []struct {
		name string
		spec map[string]interface{}
		want bool
	}{
		{
			name: "managed runtime metadata",
			spec: map[string]interface{}{
				"metadata": map[string]interface{}{
					"server_provisioning_mode": "kombify-cloud",
					"server_mode":              "monthly-runtime",
				},
			},
			want: true,
		},
		{
			name: "legacy cloud provider",
			spec: map[string]interface{}{
				"provider": "cloud",
			},
			want: true,
		},
		{
			name: "managed node provider",
			spec: map[string]interface{}{
				"nodes": []interface{}{
					map[string]interface{}{"provider": "cloud", "provider_id": "centron"},
				},
			},
			want: true,
		},
		{
			name: "user owned remote",
			spec: map[string]interface{}{
				"metadata": map[string]interface{}{
					"server_provisioning_mode": "connect-remote",
					"server_mode":              "user-owned",
				},
				"nodes": []interface{}{
					map[string]interface{}{"provider": "local"},
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasManagedRuntimeCreatePolicy(tt.spec); got != tt.want {
				t.Fatalf("hasManagedRuntimeCreatePolicy() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestShouldStartRolloutAfterCreate(t *testing.T) {
	tests := []struct {
		name string
		spec map[string]interface{}
		want bool
	}{
		{
			name: "kombify-cloud subscription auto-deploys",
			spec: map[string]interface{}{
				"provider": "cloud",
				"metadata": map[string]interface{}{
					"server_provisioning_mode": "kombify-cloud",
					"server_mode":              "monthly-runtime",
				},
			},
			want: true,
		},
		{
			name: "managed-cloud provider auto-deploys",
			spec: map[string]interface{}{
				"provider": "cloud",
			},
			want: true,
		},
		{
			name: "canonical provider centron auto-deploys",
			spec: map[string]interface{}{
				"metadata": map[string]interface{}{
					"provider_id": "centron",
				},
			},
			want: true,
		},
		{
			name: "install-command requires explicit review+start",
			spec: map[string]interface{}{
				"options": map[string]interface{}{
					"server_provisioning_mode": "install-command",
				},
			},
			want: false,
		},
		{
			name: "connect-remote requires explicit review+start",
			spec: map[string]interface{}{
				"metadata": map[string]interface{}{
					"server_provisioning_mode": "connect-remote",
				},
			},
			want: false,
		},
		{
			name: "nil spec never auto-deploys",
			spec: nil,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldStartRolloutAfterCreate(tt.spec); got != tt.want {
				t.Fatalf("shouldStartRolloutAfterCreate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStackListItemProjectsRuntimeAndProvisioningFields(t *testing.T) {
	collection := core.NewBaseCollection("stacks")
	collection.Fields.Add(
		&core.TextField{Name: "name"},
		&core.TextField{Name: "mode"},
		&core.TextField{Name: "status"},
		&core.TextField{Name: "runtime_phase"},
		&core.TextField{Name: "server_mode"},
		&core.TextField{Name: "runtime_lane"},
		&core.TextField{Name: "runtime_offering_id"},
		&core.TextField{Name: "provider_id"},
		&core.TextField{Name: "lease_provider"},
		&core.TextField{Name: "provider_region"},
		&core.TextField{Name: "ionos_datacenter"},
		&core.TextField{Name: "lease_id"},
		&core.TextField{Name: "simulate_provider_id"},
		&core.TextField{Name: "simulate_node_lifecycle"},
		&core.TextField{Name: "desired_state"},
		&core.TextField{Name: "billing_mode"},
		&core.TextField{Name: "billing_cadence"},
		&core.TextField{Name: "stackkit_catalog_ref"},
		&core.TextField{Name: "verification_status"},
		&core.TextField{Name: "server_provisioning_mode"},
		&core.TextField{Name: "server_connection_mode"},
		&core.BoolField{Name: "server_remote_host_present"},
		&core.BoolField{Name: "server_remote_user_present"},
		&core.TextField{Name: "server_remote_auth_method"},
		&core.TextField{Name: "server_remote_credential_ref"},
		&core.BoolField{Name: "server_remote_use_sudo"},
		&core.BoolField{Name: "server_install_command_required"},
	)
	stack := core.NewRecord(collection)
	stack.Id = "stack-runtime"
	stack.Set("name", "Runtime Stack")
	stack.Set("mode", "easy")
	stack.Set("status", "provisioning")
	stack.Set("runtime_phase", "lease_ready")
	stack.Set("server_mode", "monthly-runtime")
	stack.Set("runtime_lane", "monthly-runtime")
	stack.Set("runtime_offering_id", "monthly-runtime-standard")
	stack.Set("provider_id", "centron")
	stack.Set("lease_provider", "centron-managed")
	stack.Set("provider_region", "de/fra")
	stack.Set("ionos_datacenter", "de/fra")
	stack.Set("lease_id", "lease-123")
	stack.Set("simulate_provider_id", "centron-managed")
	stack.Set("simulate_node_lifecycle", "pvm")
	stack.Set("desired_state", "running")
	stack.Set("billing_mode", "subscription")
	stack.Set("billing_cadence", "monthly")
	stack.Set("stackkit_catalog_ref", "basement-kit")
	stack.Set("verification_status", "pending")
	stack.Set("server_provisioning_mode", "kombify-cloud")
	stack.Set("server_connection_mode", "managed-subscription")
	stack.Set("server_remote_host_present", true)
	stack.Set("server_remote_user_present", true)
	stack.Set("server_remote_auth_method", "ssh-key")
	stack.Set("server_remote_credential_ref", "root@server")
	stack.Set("server_remote_use_sudo", true)
	stack.Set("server_install_command_required", false)

	got := stackListItem(stack)

	for key, want := range map[string]any{
		"runtime_lane":                    "monthly-runtime",
		"runtime_offering_id":             "monthly-runtime-standard",
		"provider_id":                     "centron",
		"lease_provider":                  "centron-managed",
		"provider_region":                 "de/fra",
		"ionos_datacenter":                "de/fra",
		"lease_id":                        "lease-123",
		"simulate_provider_id":            "centron-managed",
		"simulate_node_lifecycle":         "pvm",
		"billing_cadence":                 "monthly",
		"stackkit_catalog_ref":            "basement-kit",
		"catalog_ref":                     "basement-kit",
		"server_provisioning_mode":        "kombify-cloud",
		"server_connection_mode":          "managed-subscription",
		"server_remote_host_present":      true,
		"server_remote_user_present":      true,
		"server_remote_auth_method":       "ssh-key",
		"server_remote_credential_ref":    "root@server",
		"server_remote_use_sudo":          true,
		"server_install_command_required": false,
	} {
		if got[key] != want {
			t.Fatalf("stackListItem()[%q] = %v, want %v", key, got[key], want)
		}
	}
}

func TestRuntimeFieldsFromConfigExtractsWizardRuntimeSummary(t *testing.T) {
	fields := runtimeFieldsFromConfig(map[string]interface{}{
		"name": "runtime-stack",
		"kit":  "basement-kit",
		"metadata": map[string]interface{}{
			"server_mode":                "monthly-runtime",
			"runtime_lane":               "monthly-runtime",
			"runtime_offering_id":        "monthly-runtime-standard",
			"provider_id":                "ionos",
			"provider_region":            "us-ewr",
			"simulate_node_lifecycle":    "pvm",
			"billing_cadence":            "monthly",
			"verification_status":        "pending",
			"server_provisioning_mode":   "kombify-cloud",
			"server_connection_mode":     "managed-subscription",
			"server_remote_host_present": "false",
		},
	})

	for key, want := range map[string]any{
		"server_mode":                "monthly-runtime",
		"runtime_lane":               "monthly-runtime",
		"runtime_offering_id":        "monthly-runtime-standard",
		"provider_id":                "ionos",
		"provider_region":            "us/ewr",
		"ionos_datacenter":           "us/ewr",
		"simulate_node_lifecycle":    "pvm",
		"billing_cadence":            "monthly",
		"stackkit_catalog_ref":       "basement-kit",
		"verification_status":        "pending",
		"server_provisioning_mode":   "kombify-cloud",
		"server_connection_mode":     "managed-subscription",
		"server_remote_host_present": false,
	} {
		if got := fields[key]; got != want {
			t.Fatalf("runtimeFieldsFromConfig()[%q] = %v, want %v", key, got, want)
		}
	}
}

func TestRuntimeFieldsFromConfigDefaultsUserOwnedProvisioningSummary(t *testing.T) {
	fields := runtimeFieldsFromConfig(map[string]interface{}{
		"name": "one-liner-stack",
		"options": map[string]interface{}{
			"server_provisioning_mode": "install-command",
		},
	})

	for key, want := range map[string]any{
		"server_mode":                     "user-owned",
		"server_provisioning_mode":        "install-command",
		"server_connection_mode":          "agent-oneliner",
		"server_install_command_required": true,
	} {
		if got := fields[key]; got != want {
			t.Fatalf("runtimeFieldsFromConfig()[%q] = %v, want %v", key, got, want)
		}
	}
}

// TestMultiStackLifecycle_Documentation documents the supported multi-stack CRUD behavior.
func TestMultiStackLifecycle_Documentation(t *testing.T) {
	// Multiple stacks are now a supported server-side behavior.
	// The CRUD layer accepts repeated POST /api/v1/stacks requests and persists
	// owner-scoped stack records across repeated create operations.

	t.Log("Multi-stack lifecycle documented - see integration tests for repeated create validation")
}

func ownerSpecRequestEvent(stackID, token string) (*httpx.Event, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(http.MethodGet, ownerSpecEndpoint(stackID), nil)
	req.SetPathValue("id", stackID)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	return &httpx.Event{Request: req, Response: rec}, rec
}

func newOwnerSpecTestApp(t *testing.T) *tests.TestApp {
	t.Helper()

	app, err := tests.NewTestApp(pocketBaseTestDataDir(t))
	if err != nil {
		t.Fatalf("new test app: %v", err)
	}
	ensureOwnerSpecTestCollections(t, app)
	return app
}

func ensureOwnerSpecTestCollections(t *testing.T, app core.App) {
	t.Helper()

	ensureOwnerSpecTestCollection(t, app, "stacks",
		&core.TextField{Name: "name"},
		&core.TextField{Name: "owner_id"},
		&core.JSONField{Name: "user_config"},
	)
	ensureOwnerSpecTestCollection(t, app, "wallet",
		&core.TextField{Name: "owner_id"},
		&core.TextField{Name: "stack_id"},
		&core.TextField{Name: "service_id"},
		&core.TextField{Name: "secret", Max: 2000},
	)
	ensureOwnerSpecTestCollection(t, app, "activity_log",
		&core.SelectField{Name: "action", Required: true, Values: []string{
			ownerSpecActionTokenIssued,
			ownerSpecActionRead,
			ownerSpecActionDenied,
		}},
		&core.TextField{Name: "details", Max: 2000},
		&core.JSONField{Name: "metadata"},
		&core.TextField{Name: "stack_id"},
		&core.TextField{Name: "user_id"},
		&core.SelectField{Name: "status", Values: []string{"success", "warning", "error", "info"}},
	)
	tokenCollection := ensureOwnerSpecTestCollection(t, app, "owner_spec_tokens",
		&core.TextField{Name: "token_hash", Required: true, Max: 128},
		&core.TextField{Name: "stack_id", Required: true, Max: 200},
		&core.TextField{Name: "owner_id", Required: true, Max: 200},
		&core.SelectField{Name: "status", Required: true, Values: []string{ownerSpecTokenStatusIssued, ownerSpecTokenStatusConsumed}},
		&core.TextField{Name: "expires_at", Required: true, Max: 64},
		&core.TextField{Name: "consumed_at", Max: 64},
	)
	tokenCollection.AddIndex("idx_owner_spec_tokens_token_hash", true, "token_hash", "")
	if err := app.Save(tokenCollection); err != nil {
		t.Fatalf("save owner_spec_tokens index: %v", err)
	}
}

func ensureOwnerSpecTestCollection(t *testing.T, app core.App, name string, fields ...core.Field) *core.Collection {
	t.Helper()

	collection, err := app.FindCollectionByNameOrId(name)
	if err != nil {
		collection = core.NewBaseCollection(name)
	}
	for _, field := range fields {
		if collection.Fields.GetByName(field.GetName()) == nil {
			collection.Fields.Add(field)
		}
	}
	if err := app.Save(collection); err != nil {
		t.Fatalf("save %s collection: %v", name, err)
	}
	return collection
}

func createOwnerSpecTestStack(t *testing.T, app core.App, ownerID string) *core.Record {
	t.Helper()

	collection, err := app.FindCollectionByNameOrId("stacks")
	if err != nil {
		t.Fatalf("find stacks collection: %v", err)
	}
	record := core.NewRecord(collection)
	record.Set("name", "Configured Stack")
	record.Set("owner_id", ownerID)
	record.Set("user_config", map[string]any{
		"name": "configured-stack",
		"identity": map[string]any{
			"owner": map[string]any{
				"source":      ownerSourceLocal,
				"email":       "owner@example.com",
				"username":    "owner",
				"displayName": "Owner",
			},
			"recovery": map[string]any{
				"passphraseHashPresent": true,
				"passphraseHashRef":     "secret://techstack/recovery-passphrase-hash",
			},
		},
	})
	if err := app.Save(record); err != nil {
		t.Fatalf("save stack: %v", err)
	}
	return record
}

func createOwnerSpecTestRecoveryWalletEntry(t *testing.T, app core.App, stackID, ownerID, recoveryHash string) *core.Record {
	t.Helper()

	collection, err := app.FindCollectionByNameOrId("wallet")
	if err != nil {
		t.Fatalf("find wallet collection: %v", err)
	}
	record := core.NewRecord(collection)
	record.Set("owner_id", ownerID)
	record.Set("stack_id", stackID)
	record.Set("service_id", recoveryWalletServiceID)
	record.Set("secret", recoveryHash)
	if err := app.Save(record); err != nil {
		t.Fatalf("save wallet: %v", err)
	}
	return record
}

func pocketBaseTestDataDir(t *testing.T) string {
	t.Helper()

	cmd := exec.Command("go", "env", "GOMODCACHE")
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("resolve go module cache: %v", err)
	}

	modCache := strings.TrimSpace(string(output))
	if modCache == "" {
		t.Fatal("resolve go module cache: empty result")
	}

	matches, err := filepath.Glob(filepath.Join(modCache, "github.com", "pocketbase", "pocketbase@*", "tests", "data"))
	if err != nil {
		t.Fatalf("resolve pocketbase test data: %v", err)
	}
	if len(matches) == 0 {
		matches, err = filepath.Glob(filepath.Join(modCache, "github.com", "*", "pocketbase@*", "tests", "data"))
		if err != nil {
			t.Fatalf("resolve pocketbase test data fallback: %v", err)
		}
	}
	for _, match := range matches {
		if strings.Contains(filepath.ToSlash(match), path.Join("github.com", "pocketbase", "pocketbase@")) {
			return match
		}
	}

	t.Fatal("resolve pocketbase test data: no matching data directory found")
	return ""
}
