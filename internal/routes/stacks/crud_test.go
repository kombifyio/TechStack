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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"

	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/controlplane"
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

func TestNormalizeCreateStackRequestRejectsPlaintextRecoveryMaterial(t *testing.T) {
	for _, test := range []struct {
		name, nestedKey string
		option, rawYAML bool
	}{
		{name: "options passphrase", option: true},
		{name: "identity recovery passphrase", nestedKey: "passphrase"},
		{name: "identity recovery plaintext", nestedKey: "plaintext"},
		{name: "raw YAML passphrase", rawYAML: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := createStackRequest{
				Name: "Plaintext Recovery", Mode: "easy",
				StackSpec: map[string]interface{}{
					"name": "plaintext-recovery",
					"metadata": map[string]interface{}{
						"created_by": "wizard", "owner_bootstrap_mode": "auto", "owner_source": "local",
					},
				},
				Options: map[string]interface{}{"owner_bootstrap_mode": "auto", "owner_source": "local"},
			}
			if test.option {
				req.Options["recovery_passphrase"] = "correct horse battery staple"
			}
			if test.nestedKey != "" {
				req.StackSpec["identity"] = map[string]interface{}{
					"recovery": map[string]interface{}{test.nestedKey: "correct horse battery staple"},
				}
			}
			if test.rawYAML {
				req = createStackRequest{Name: "Raw YAML Recovery", Mode: "easy", UserConfigFormat: "yaml", UserConfigRaw: `
name: raw-yaml-recovery
identity:
  recovery:
    passphrase_hash: ` + testRecoveryPassphraseHash + `
    passphrase: correct horse battery staple
				`}
			}
			if _, msg := normalizeCreateStackRequest(req); msg == "" {
				t.Fatal("normalizeCreateStackRequest() accepted plaintext recovery material")
			}
		})
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

func TestNormalizeCreateStackRequest(t *testing.T) {
	tests := []struct {
		name         string
		req          createStackRequest
		wantName     string
		wantMode     string
		wantMsg      string
		wantJobField string
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
			wantName:     "Homelab",
			wantMode:     "techie",
			wantJobField: "provider",
		},
		{
			name: "raw config allowed without user_config",
			req: createStackRequest{
				UserConfigRaw:    `{"provider":"proxmox"}`,
				UserConfigFormat: "json",
			},
			wantName:     "homelab",
			wantMode:     "easy",
			wantJobField: "provider",
		},
		{
			name: "raw YAML config becomes the job spec",
			req: createStackRequest{
				UserConfigRaw:    "provider: proxmox",
				UserConfigFormat: "yaml",
			},
			wantName:     "homelab",
			wantMode:     "easy",
			wantJobField: "provider",
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
			if tt.wantJobField != "" && createStackJobSpec(got)[tt.wantJobField] == nil {
				t.Fatalf("expected create job spec field %q", tt.wantJobField)
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
			name: "user-owned basement stays basement",
			req: createStackRequest{
				Name: "Local Stack",
				Mode: "easy",
				StackSpec: map[string]interface{}{
					"name":     "local-stack",
					"stackkit": "basement-kit",
					"metadata": map[string]interface{}{
						"stackkit_catalog_ref": "basement-kit",
					},
				},
			},
			want: "basement-kit",
		},
		{
			name: "managed cloud stays cloud",
			req: createStackRequest{
				Name:     "Managed Stack",
				Mode:     "easy",
				Provider: "cloud",
				StackSpec: map[string]interface{}{
					"name":     "managed-stack",
					"stackkit": "cloud-kit",
					"metadata": map[string]interface{}{
						"server_provisioning_mode": "kombify-cloud",
						"provider_id":              "ionos",
						"stackkit_catalog_ref":     "cloud-kit",
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

	normalized, msg := normalizeCreateStackRequest(req)
	if msg != "" {
		t.Fatalf("normalizeCreateStackRequest() message = %q, want empty so signed-in email can fill later", msg)
	}
	bootstrap, ok := ownerBootstrapFromRequest(normalized)
	if !ok || bootstrap.Source != ownerSourceLocal || bootstrap.Email != "" {
		t.Fatalf("expected custom local bootstrap with omitted email, got %+v", bootstrap)
	}

	req.Options = map[string]interface{}{
		"owner_bootstrap_mode": "auto",
		"owner_source":         "local",
	}
	normalized, msg = normalizeCreateStackRequest(req)
	if msg != "" {
		t.Fatalf("normalizeCreateStackRequest(auto) message = %q, want empty", msg)
	}
	bootstrap, ok = ownerBootstrapFromRequest(normalized)
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
		Email:          "owner@example.com",
		DisplayName:    "Owner Example",
		CurrentProfile: &cloudLinkIdentity{Email: "owner@example.com", EmailVerified: true, DisplayName: "Owner Example"},
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
		bootstrap.Email != "owner@example.com" ||
		bootstrap.Username != "owner" ||
		bootstrap.DisplayName != "Owner Example" {
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
		Email:          "cloud.owner@example.com",
		Username:       "auth0|cloud-subject",
		DisplayName:    "Cloud Owner",
		CurrentProfile: &cloudLinkIdentity{Email: "cloud.owner@example.com", EmailVerified: true, DisplayName: "Cloud Owner"},
	})
	if denial != nil {
		t.Fatalf("resolveCreateOwnerBootstrap() denial = %q, want none", denial.Message)
	}

	bootstrap, ok := ownerBootstrapFromRequest(resolved)
	if !ok {
		t.Fatal("expected resolved owner bootstrap")
	}
	if bootstrap.Source != ownerSourceCloud ||
		bootstrap.Email != "cloud.owner@example.com" ||
		bootstrap.Username == "stale-client" ||
		bootstrap.DisplayName != "Cloud Owner" {
		t.Fatalf("automatic cloud bootstrap must use the verified profile, got %+v", bootstrap)
	}
	if resolved.Options["owner_email"] != "cloud.owner@example.com" {
		t.Fatalf("cloud auto bootstrap must replace stale owner_email: %+v", resolved.Options)
	}
	owner, ok := resolved.UserConfig["owner"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected owner map in resolved spec: %+v", resolved.UserConfig["owner"])
	}
	if owner["source"] != ownerSourceCloud || owner["bootstrapMode"] != ownerBootstrapModeAuto {
		t.Fatalf("expected cloud auto owner handoff for StackKits, got %+v", owner)
	}
	if owner["email"] != "cloud.owner@example.com" {
		t.Fatalf("cloud auto owner spec must use the verified email: %+v", owner)
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

func TestResolveCreateOwnerBootstrap_CustomLocalUsesSignedInEmailWhenOmitted(t *testing.T) {
	normalized := customLocalOwnerRequest("")
	resolved, denial := resolveCreateOwnerBootstrap(normalized, ownerBootstrapContext{
		Email:       "cloud.owner@example.com",
		DisplayName: "Cloud Owner",
	})
	if denial != nil {
		t.Fatalf("resolveCreateOwnerBootstrap() denial = %q, want none", denial.Message)
	}
	bootstrap, ok := ownerBootstrapFromRequest(resolved)
	if !ok {
		t.Fatal("expected resolved owner bootstrap")
	}
	if bootstrap.Email != "cloud.owner@example.com" ||
		bootstrap.Username != "cloud-owner" ||
		bootstrap.DisplayName != "Cloud Owner" {
		t.Fatalf("omitted custom email should inherit the signed-in account, got %+v", bootstrap)
	}
}

func TestResolveCreateOwnerBootstrap_CustomLocalKeepsExplicitEmail(t *testing.T) {
	normalized := customLocalOwnerRequest("other.owner@example.com")
	resolved, denial := resolveCreateOwnerBootstrap(normalized, ownerBootstrapContext{
		Email:       "cloud.owner@example.com",
		DisplayName: "Cloud Owner",
	})
	if denial != nil {
		t.Fatalf("resolveCreateOwnerBootstrap() denial = %q, want none", denial.Message)
	}
	bootstrap, ok := ownerBootstrapFromRequest(resolved)
	if !ok {
		t.Fatal("expected resolved owner bootstrap")
	}
	if bootstrap.Email != "other.owner@example.com" || bootstrap.Username != "other-owner" {
		t.Fatalf("explicit custom email should win over the signed-in account, got %+v", bootstrap)
	}
	if bootstrap.DisplayName == "Cloud Owner" {
		t.Fatalf("explicit custom email must not inherit the signed-in display name, got %+v", bootstrap)
	}
}

func TestResolveCreateOwnerBootstrap_CustomLocalMissingEmailWithoutSessionFails(t *testing.T) {
	normalized := customLocalOwnerRequest("")
	_, denial := resolveCreateOwnerBootstrap(normalized, ownerBootstrapContext{})
	if denial == nil || denial.ReasonCode != reasonOwnerEmailMissing {
		t.Fatalf("resolveCreateOwnerBootstrap() denial = %+v, want owner_email_missing", denial)
	}
}

func customLocalOwnerRequest(email string) normalizedCreateStackRequest {
	options := map[string]interface{}{
		"owner_bootstrap_mode": "custom",
		"owner_source":         "local",
	}
	if email != "" {
		options["owner_email"] = email
	}
	return normalizedCreateStackRequest{
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
		Options: options,
	}
}

func TestNormalizeCreateStackRequest_AutomaticCloudOwnerRejectsBrowserIdentity(t *testing.T) {
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
			"owner_bootstrap_mode": "auto",
			"owner_source":         "cloud",
			"owner_email":          "owner@example.com",
		},
	})

	if !strings.Contains(msg, "not accepted for an automatic Cloud owner") {
		t.Fatalf("normalizeCreateStackRequest() message = %q, want client identity rejection", msg)
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

	recoveryFields, err := recoveryWalletFields("user-1", "stack-1", "Configured Stack", bootstrap)
	if err != nil {
		t.Fatalf("recoveryWalletFields failed: %v", err)
	}
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

	token, expiresAt, err := issueOwnerSpecBootstrapTokenForTenant("tenant-1", "stack-1", "owner-1", now)
	if err != nil {
		t.Fatalf("issueOwnerSpecBootstrapTokenForTenant() error = %v", err)
	}
	if expiresAt.Sub(now) != ownerSpecBootstrapTokenTTL {
		t.Fatalf("expiresAt delta = %s, want %s", expiresAt.Sub(now), ownerSpecBootstrapTokenTTL)
	}

	claims, err := verifyOwnerSpecBootstrapToken(token, "stack-1", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("verifyOwnerSpecBootstrapToken() error = %v", err)
	}
	if claims.TenantID != "tenant-1" || claims.StackID != "stack-1" || claims.OwnerID != "owner-1" || !hasOwnerSpecScope(claims.Scopes) {
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

	expiredToken, _, err := issueOwnerSpecBootstrapTokenForTenant("tenant-1", "stack-1", "owner-1", now.Add(-ownerSpecBootstrapTokenTTL-time.Minute))
	if err != nil {
		t.Fatalf("issue expired token: %v", err)
	}

	crossStackToken, _, err := issueOwnerSpecBootstrapTokenForTenant("tenant-1", "stack-a", "owner-1", time.Now().UTC())
	if err != nil {
		t.Fatalf("issue cross-stack token: %v", err)
	}

	tests := []struct {
		name       string
		stackID    string
		token      string
		wantStatus int
	}{
		{name: "missing token", stackID: "stack-1", wantStatus: http.StatusUnauthorized},
		{name: "expired token", stackID: "stack-1", token: expiredToken, wantStatus: http.StatusUnauthorized},
		{name: "cross-stack token", stackID: "stack-b", token: crossStackToken, wantStatus: http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event, recorder := ownerSpecRequestEvent(tt.stackID, tt.token)
			if ownerSpecErr := h.ownerSpec(event); ownerSpecErr != nil {
				t.Fatalf("ownerSpec() error = %v", ownerSpecErr)
			}
			if recorder.Code != tt.wantStatus {
				t.Fatalf("status = %d body=%s, want %d", recorder.Code, recorder.Body.String(), tt.wantStatus)
			}
		})
	}
}

func TestOwnerSpecEndpointReturnsStackKitIdentityRecoverySchema(t *testing.T) {
	t.Setenv(ownerSpecBootstrapTokenSecretEnv, "0123456789abcdef0123456789abcdef")
	h, access := ownerSpecCanonicalFixture(t)

	event, recorder := ownerSpecRequestEvent("stack-1", access.Token)
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
	if envelope.Data.StackID != "stack-1" {
		t.Fatalf("stack_id = %q, want stack-1", envelope.Data.StackID)
	}
	if envelope.Data.Identity.Owner.Source != ownerSourceLocal ||
		envelope.Data.Identity.Owner.Email != "owner@example.com" ||
		envelope.Data.Identity.Owner.Username != "owner" ||
		envelope.Data.Identity.Owner.DisplayName != "Owner" {
		t.Fatalf("unexpected owner response: %+v", envelope.Data.Identity.Owner)
	}
	if envelope.Data.Identity.Recovery.PassphraseHash != testRecoveryPassphraseHash || !envelope.Data.Identity.Recovery.PassphraseHashPresent {
		t.Fatalf("unexpected recovery response: %+v", envelope.Data.Identity.Recovery)
	}
	if !hasOwnerSpecScope(envelope.Data.Scopes) {
		t.Fatalf("expected owner spec scope in response, got %v", envelope.Data.Scopes)
	}
	events, err := h.activityStore.ListActivity(t.Context(), "owner-1", "stack-1", 10)
	if err != nil || len(events) != 2 {
		t.Fatalf("canonical owner-spec audit events = %#v, err=%v", events, err)
	}
}

func TestOwnerSpecTokenIsSingleUse(t *testing.T) {
	t.Setenv(ownerSpecBootstrapTokenSecretEnv, "0123456789abcdef0123456789abcdef")
	h, access := ownerSpecCanonicalFixture(t)

	firstEvent, firstRecorder := ownerSpecRequestEvent("stack-1", access.Token)
	if ownerSpecErr := h.ownerSpec(firstEvent); ownerSpecErr != nil {
		t.Fatalf("ownerSpec(first) error = %v", ownerSpecErr)
	}
	if firstRecorder.Code != http.StatusOK {
		t.Fatalf("first ownerSpec() status = %d body=%s, want 200", firstRecorder.Code, firstRecorder.Body.String())
	}

	secondEvent, secondRecorder := ownerSpecRequestEvent("stack-1", access.Token)
	if ownerSpecErr := h.ownerSpec(secondEvent); ownerSpecErr != nil {
		t.Fatalf("ownerSpec(second) error = %v", ownerSpecErr)
	}
	if secondRecorder.Code != http.StatusForbidden {
		t.Fatalf("second ownerSpec() status = %d body=%s, want 403", secondRecorder.Code, secondRecorder.Body.String())
	}
}

func ownerSpecCanonicalFixture(t *testing.T) (crudRouteHandlers, ownerSpecBootstrapAccess) {
	t.Helper()
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(t.Context(), controlplane.CreateStackRequest{
		ID: "stack-1", TenantID: "owner-1", OwnerSubjectID: "owner-1", Config: map[string]any{
			"owner": map[string]any{"bootstrapMode": ownerBootstrapModeCustom, "source": ownerSourceLocal, "email": "owner@example.com", "username": "owner", "displayName": "Owner"},
		},
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	if _, err := store.UpsertWalletItem(t.Context(), controlplane.WalletItem{
		ID: "stack-1:" + recoveryWalletServiceID, TenantID: "owner-1", StackID: "stack-1", Metadata: map[string]any{"secret": testRecoveryPassphraseHash},
	}); err != nil {
		t.Fatalf("UpsertWalletItem: %v", err)
	}
	h := crudRouteHandlers{stackStore: store, walletStore: store, activityStore: store}
	access, err := h.issueOwnerSpecBootstrapAccessForTenant(t.Context(), "owner-1", "stack-1", "owner-1", time.Now().UTC())
	if err != nil {
		t.Fatalf("issueOwnerSpecBootstrapAccessForTenant: %v", err)
	}
	return h, access
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

func TestValidateDeploymentLaneManagedRuntimeModes(t *testing.T) {
	tests := []struct {
		name        string
		environment string
		localE2E    bool
		wantAllowed bool
	}{
		{name: "self hosted rejects", environment: "production"},
		{name: "development E2E allows", environment: "development", localE2E: true, wantAllowed: true},
		{name: "production ignores E2E override", environment: "production", localE2E: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("TECHSTACK_ENV", test.environment)
			t.Setenv("TECHSTACK_ALLOW_LOCAL_SIMULATION_GATE", strconv.FormatBool(test.localE2E))
			t.Setenv("TECHSTACK_ALLOW_LOCAL_MANAGED_RUNTIME_E2E", strconv.FormatBool(test.localE2E))

			normalized, msg := normalizeCreateStackRequest(managedRuntimeCreateRequest("centron"))
			if msg != "" {
				t.Fatalf("normalizeCreateStackRequest() message = %q, want empty", msg)
			}
			allowed := validateDeploymentLane(normalized, config.ModeSelfHosted) == ""
			if allowed != test.wantAllowed {
				t.Fatalf("validateDeploymentLane() allowed = %t, want %t", allowed, test.wantAllowed)
			}
		})
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

func TestValidateDeploymentLaneAllowsSupportedModes(t *testing.T) {
	tests := []struct {
		name string
		mode config.DeploymentMode
		req  createStackRequest
	}{
		{
			name: "saas remote ssh",
			mode: config.ModeSaaS,
			req: deploymentLaneTestRequest("", map[string]interface{}{
				"server_provisioning_mode":   "connect-remote",
				"server_connection_mode":     "remote-ssh",
				"server_remote_host_present": "true",
				"server_remote_user_present": "true",
				"server_mode":                "user-owned",
				"billing_mode":               "local",
			}),
		},
		{
			name: "saas install command",
			mode: config.ModeSaaS,
			req: deploymentLaneTestRequest("", map[string]interface{}{
				"server_provisioning_mode":        "install-command",
				"server_connection_mode":          "agent-oneliner",
				"server_install_command_required": "true",
				"server_mode":                     "self-hosted",
				"billing_mode":                    "local",
			}),
		},
		{
			name: "self hosted install command",
			mode: config.ModeSelfHosted,
			req: deploymentLaneTestRequest("", map[string]interface{}{
				"server_provisioning_mode": "install-command",
				"server_mode":              "user-owned",
				"billing_mode":             "local",
			}),
		},
		{
			name: "self hosted remote ssh",
			mode: config.ModeSelfHosted,
			req: deploymentLaneTestRequest("", map[string]interface{}{
				"server_provisioning_mode": "connect-remote",
				"server_connection_mode":   "remote-ssh",
				"server_mode":              "user-owned",
			}),
		},
		{
			name: "saas managed runtime",
			mode: config.ModeSaaS,
			req: deploymentLaneTestRequest("centron", map[string]interface{}{
				"server_provisioning_mode": "kombify-cloud",
				"server_connection_mode":   "managed-subscription",
				"server_mode":              "monthly-runtime",
				"runtime_lane":             "monthly-runtime",
				"billing_mode":             "subscription",
			}),
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

func deploymentLaneTestRequest(providerID string, metadata map[string]interface{}) createStackRequest {
	return createStackRequest{
		Name: "Lane Test", Mode: "easy", ProviderID: providerID,
		StackSpec: map[string]interface{}{"name": "lane-test", "metadata": metadata},
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
