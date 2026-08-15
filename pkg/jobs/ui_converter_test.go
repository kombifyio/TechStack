// Package jobs provides unit tests for UI converter functions.
package jobs

import (
	"errors"
	"testing"

	"github.com/kombifyio/techstack/internal/providercatalog"
)

// TestMapWizardProviderToUnifier tests all provider mapping cases.
func TestMapWizardProviderToUnifier(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		want     string
	}{
		{"homelab maps to local", "homelab", "local"},
		{"cloud remains a non-authoritative UI mode", "cloud", "cloud"},
		{"local maps to local", "local", "local"},
		{"unknown passes through", "proxmox", "proxmox"},
		{"aws passes through", "aws", "aws"},
		{"digitalocean passes through", "digitalocean", "digitalocean"},
		{"hetzner passes through", "hetzner", "hetzner"},
		{"empty string", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mapWizardProviderToUnifier(tt.provider)
			if got != tt.want {
				t.Errorf("mapWizardProviderToUnifier(%q) = %q, want %q", tt.provider, got, tt.want)
			}
		})
	}
}

// TestMapGoalsToServices tests goal-to-service mapping.
func TestMapGoalsToServices(t *testing.T) {
	tests := []struct {
		name             string
		goals            map[string]interface{}
		wantMinServices  int
		wantServiceNames []string
	}{
		{
			name:             "empty goals",
			goals:            map[string]interface{}{},
			wantMinServices:  0,
			wantServiceNames: nil,
		},
		{
			name: "websiteEmail goal",
			goals: map[string]interface{}{
				"websiteEmail": true,
			},
			wantMinServices:  1,
			wantServiceNames: []string{"traefik"},
		},
		{
			name: "storage goal",
			goals: map[string]interface{}{
				"storage": true,
			},
			wantMinServices:  8,
			wantServiceNames: []string{"traefik", "pocket-id", "vaultwarden", "immich-server", "immich-ml", "immich-postgres", "immich-redis", "otel-collector"},
		},
		{
			name: "everything goal",
			goals: map[string]interface{}{
				"everything": true,
			},
			wantMinServices:  8,
			wantServiceNames: []string{"traefik", "pocket-id", "vaultwarden", "immich-server", "immich-ml", "immich-postgres", "immich-redis", "otel-collector"},
		},
		{
			name: "multiple goals (websiteEmail + storage)",
			goals: map[string]interface{}{
				"websiteEmail": true,
				"storage":      true,
			},
			wantMinServices:  8,
			wantServiceNames: []string{"traefik", "pocket-id", "vaultwarden", "immich-server", "immich-ml", "immich-postgres", "immich-redis", "otel-collector"},
		},
		{
			name: "false goals are ignored",
			goals: map[string]interface{}{
				"websiteEmail": false,
				"storage":      false,
			},
			wantMinServices: 0,
		},
		{
			name: "invalid goal types are ignored",
			goals: map[string]interface{}{
				"websiteEmail": "not a bool",
				"storage":      123,
			},
			wantMinServices: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			services := mapGoalsToServices(tt.goals)

			if len(services) < tt.wantMinServices {
				t.Errorf("mapGoalsToServices() returned %d services, want at least %d", len(services), tt.wantMinServices)
			}

			// Check that expected services are present
			for _, wantName := range tt.wantServiceNames {
				found := false
				for _, svc := range services {
					if svc.Name == wantName {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected service %q not found in result", wantName)
				}
			}
		})
	}
}

// TestMapAccessModeToNetwork tests access mode to network config mapping.
func TestMapAccessModeToNetwork(t *testing.T) {
	tests := []struct {
		name       string
		accessMode string
		wantVPN    string
		wantDomain string
	}{
		{
			name:       "local access",
			accessMode: "local",
			wantVPN:    "none",
			wantDomain: "",
		},
		{
			name:       "anywhere access",
			accessMode: "anywhere",
			wantVPN:    "tailscale",
			wantDomain: "ts.net",
		},
		{
			name:       "public access",
			accessMode: "public",
			wantVPN:    "none",
			wantDomain: "",
		},
		{
			name:       "unknown defaults to tailscale",
			accessMode: "unknown",
			wantVPN:    "tailscale",
			wantDomain: "local",
		},
		{
			name:       "empty defaults to tailscale",
			accessMode: "",
			wantVPN:    "tailscale",
			wantDomain: "local",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			network := mapAccessModeToNetwork(tt.accessMode)

			if network.VPN != tt.wantVPN {
				t.Errorf("mapAccessModeToNetwork(%q).VPN = %q, want %q", tt.accessMode, network.VPN, tt.wantVPN)
			}
			if network.Domain != tt.wantDomain {
				t.Errorf("mapAccessModeToNetwork(%q).Domain = %q, want %q", tt.accessMode, network.Domain, tt.wantDomain)
			}
		})
	}
}

// TestGenerateRegistrationToken tests token generation.
func TestGenerateRegistrationToken(t *testing.T) {
	token1 := generateRegistrationToken("stack-1")
	token2 := generateRegistrationToken("stack-2")

	// Should have correct prefix
	if len(token1) < 3 || token1[:3] != "ks_" {
		t.Errorf("expected token to start with 'ks_', got %q", token1)
	}

	// Should be 64 hex chars + 3 prefix chars = 67 total
	expectedLen := 3 + 64
	if len(token1) != expectedLen {
		t.Errorf("expected token length %d, got %d", expectedLen, len(token1))
	}

	// Tokens should be unique
	if token1 == token2 {
		t.Error("expected unique tokens for different stacks")
	}
}

// TestSanitizeDNSName_EdgeCases tests additional DNS name sanitization edge cases.
func TestSanitizeDNSName_EdgeCases(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"already lowercase", "mystack", "mystack"},
		{"with spaces", "my stack", "my-stack"},
		{"with underscores", "my_stack", "my-stack"},
		{"mixed case", "MyStack", "mystack"},
		{"multiple dashes", "my--stack", "my-stack"},
		{"leading dash", "-mystack", "mystack"},
		{"trailing dash", "mystack-", "mystack"},
		{"special chars", "my@stack!test", "my-stack-test"},
		{"numbers only", "12345", "ks-12345"},
		{"very long name", "this-is-a-very-long-name-that-exceeds-the-maximum-dns-label-length-of-sixty-three-characters", "this-is-a-very-long-name-that-exceeds-the-maximum-dns-label-len"},
		{"unicode chars", "mÿstäck", "m-st-ck"},
		{"only special chars", "!!!@@@###", "techstack"},
		{"whitespace only", "   ", "techstack"},
		{"tabs and newlines", "\t\n", "techstack"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeDNSName(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeDNSName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestConvertUIConfigToSpec_EdgeCases tests additional spec conversion edge cases.
func TestConvertUIConfigToSpec_EdgeCases(t *testing.T) {
	t.Run("non-map input returns error", func(t *testing.T) {
		_, err := convertUIConfigToSpec("not a map")
		if err == nil {
			t.Error("expected error for non-map input")
		}
	})

	t.Run("missing name defaults to techstack", func(t *testing.T) {
		config := map[string]interface{}{
			"provider": "local",
		}
		spec, err := convertUIConfigToSpec(config)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if spec.Name != "techstack" {
			t.Errorf("expected default name 'techstack', got %q", spec.Name)
		}
	})

	t.Run("network VPN from options", func(t *testing.T) {
		config := map[string]interface{}{
			"name":     "test",
			"provider": "local",
			"options": map[string]interface{}{
				"vpn": "wireguard",
			},
		}
		spec, err := convertUIConfigToSpec(config)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if spec.Network.VPN != "wireguard" {
			t.Errorf("expected VPN 'wireguard', got %q", spec.Network.VPN)
		}
	})

	t.Run("public_access option", func(t *testing.T) {
		config := map[string]interface{}{
			"name":     "test",
			"provider": "local",
			"options": map[string]interface{}{
				"public_access": true,
			},
		}
		spec, err := convertUIConfigToSpec(config)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if spec.Network.Domain != "public" {
			t.Errorf("expected Domain 'public', got %q", spec.Network.Domain)
		}
	})

	t.Run("explicit kit is preserved", func(t *testing.T) {
		config := map[string]interface{}{
			"name":     "test",
			"provider": "local",
			"kit":      "advanced-homelab",
		}
		spec, err := convertUIConfigToSpec(config)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if spec.Kit != "advanced-homelab" {
			t.Errorf("expected Kit 'advanced-homelab', got %q", spec.Kit)
		}
	})

	t.Run("cloud provider without explicit provider id fails closed", func(t *testing.T) {
		config := map[string]interface{}{
			"name":     "test",
			"provider": "cloud",
		}
		_, err := convertUIConfigToSpec(config)
		if !errors.Is(err, providercatalog.ErrProviderIDRequired) {
			t.Fatalf("convertUIConfigToSpec() error = %v, want provider_id required", err)
		}
	})

	t.Run("wizard mode uses authoritative provider id from options", func(t *testing.T) {
		config := map[string]interface{}{
			"name":     "test",
			"provider": "cloud",
			"options": map[string]interface{}{
				"provider_id": "ionos",
			},
		}
		spec, err := convertUIConfigToSpec(config)
		if err != nil {
			t.Fatalf("convertUIConfigToSpec() error = %v", err)
		}
		if got := spec.Metadata[metadataKeyProviderID]; got != providercatalog.ProviderIONOS {
			t.Fatalf("metadata provider_id = %q, want ionos", got)
		}
		if len(spec.Nodes) != 1 || spec.Nodes[0].Provider != providercatalog.ProviderIONOS {
			t.Fatalf("nodes = %#v, want authoritative IONOS provider", spec.Nodes)
		}
	})

	t.Run("StackKits stack-spec maps to internal KombinationSpec", func(t *testing.T) {
		config := map[string]interface{}{
			"name":     "release-stack",
			"stackkit": "cloud-kit",
			"context":  "cloud",
			"domain":   "kombify.me",
			"network": map[string]interface{}{
				"mode": "public",
			},
			"vpn": map[string]interface{}{
				"enabled": true,
				"type":    "headscale",
			},
			"ssh": map[string]interface{}{
				"user": "root",
				"port": 22,
			},
			"nodes": []interface{}{
				map[string]interface{}{"name": "main", "role": "standalone"},
			},
			"services": map[string]interface{}{
				"pocketid":    map[string]interface{}{"enabled": true},
				"vaultwarden": map[string]interface{}{"enabled": true},
				"uptime_kuma": map[string]interface{}{"enabled": true},
				"immich":      map[string]interface{}{"enabled": true},
				"files":       map[string]interface{}{"enabled": false},
				"jellyfin":    map[string]interface{}{"enabled": false},
			},
			"metadata": map[string]interface{}{
				"runtime_lane": "monthly-runtime",
				"provider_id":  "centron",
			},
		}

		spec, err := convertUIConfigToSpec(config)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if spec.Kit != "cloud-kit" {
			t.Fatalf("Kit = %q, want cloud-kit", spec.Kit)
		}
		if len(spec.Nodes) != 1 || spec.Nodes[0].Type != "main" || spec.Nodes[0].Provider != "centron" {
			t.Fatalf("unexpected nodes: %#v", spec.Nodes)
		}
		if spec.Nodes[0].SSH != nil {
			t.Fatalf("managed StackKit node should not synthesize SSH from hostless defaults: %#v", spec.Nodes[0].SSH)
		}
		if spec.Network.VPN != "headscale" || spec.Network.Domain != "kombify.me" {
			t.Fatalf("unexpected network: %#v", spec.Network)
		}
		names := map[string]bool{}
		types := map[string]string{}
		for _, svc := range spec.Services {
			names[svc.Name] = true
			types[svc.Name] = svc.Type
		}
		for _, want := range []string{"pocket-id", "vaultwarden", "uptime-kuma", "immich"} {
			if !names[want] {
				t.Fatalf("missing service %q in %#v", want, spec.Services)
			}
		}
		if types["uptime-kuma"] != serviceTypeMonitoring {
			t.Fatalf("uptime-kuma type = %q, want %q", types["uptime-kuma"], serviceTypeMonitoring)
		}
		if names["jellyfin"] {
			t.Fatalf("disabled jellyfin should not be converted: %#v", spec.Services)
		}
		if spec.Metadata["enable_pocketid"] != "true" {
			t.Fatalf("enable_pocketid metadata = %q, want true", spec.Metadata["enable_pocketid"])
		}
		if spec.Metadata["enable_vaultwarden"] != "true" {
			t.Fatalf("enable_vaultwarden metadata = %q, want true", spec.Metadata["enable_vaultwarden"])
		}
		if spec.Metadata["enable_uptime_kuma"] != "true" {
			t.Fatalf("enable_uptime_kuma metadata = %q, want true", spec.Metadata["enable_uptime_kuma"])
		}
		if spec.Metadata["enable_immich"] != "true" {
			t.Fatalf("enable_immich metadata = %q, want true", spec.Metadata["enable_immich"])
		}
		if spec.Metadata["enable_files"] != "false" {
			t.Fatalf("enable_files metadata = %q, want false", spec.Metadata["enable_files"])
		}
		if spec.Metadata["enable_jellyfin"] != "false" {
			t.Fatalf("enable_jellyfin metadata = %q, want false", spec.Metadata["enable_jellyfin"])
		}
	})

	t.Run("StackKits local stack-spec keeps absent domain absent", func(t *testing.T) {
		config := map[string]interface{}{
			"name":     "local-stack",
			"stackkit": "basement-kit",
			"context":  "local",
			"network": map[string]interface{}{
				"mode": "local",
			},
			"nodes": []interface{}{
				map[string]interface{}{"name": "main", "role": "standalone"},
			},
			"metadata": map[string]interface{}{
				"address_mode": "local",
			},
		}

		spec, err := convertUIConfigToSpec(config)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if spec.Network.Domain != "" {
			t.Fatalf("Network.Domain = %q, want empty for StackKits local default", spec.Network.Domain)
		}
	})

	t.Run("cloud context preserves unmanaged infrastructure provider", func(t *testing.T) {
		config := map[string]interface{}{
			"name":     "hetzner-stack",
			"stackkit": "basement-kit",
			"context":  "cloud",
			"nodes": []interface{}{
				map[string]interface{}{"name": "main", "role": "standalone", "provider": "hetzner"},
			},
		}

		spec, err := convertUIConfigToSpec(config)
		if err != nil {
			t.Fatalf("convertUIConfigToSpec() rejected unmanaged Hetzner provider: %v", err)
		}
		if spec.Kit != DefaultBasementKitRef {
			t.Fatalf("Kit = %q, want %q", spec.Kit, DefaultBasementKitRef)
		}
		if len(spec.Nodes) != 1 || spec.Nodes[0].Provider != "hetzner" {
			t.Fatalf("nodes = %#v, want unmanaged Hetzner provider", spec.Nodes)
		}
		if spec.Metadata[metadataKeyProviderID] != "" || spec.Metadata[metadataKeyServerMode] == serverModeMonthlyRuntime {
			t.Fatalf("metadata = %#v, want no managed provider authority", spec.Metadata)
		}
	})

	t.Run("legacy base-kit stack-spec normalizes by runtime target", func(t *testing.T) {
		localSpec, err := convertUIConfigToSpec(map[string]interface{}{
			"name":     "legacy-local",
			"stackkit": "base-kit",
			"context":  "local",
			"nodes": []interface{}{
				map[string]interface{}{"name": "main", "role": "standalone"},
			},
		})
		if err != nil {
			t.Fatalf("unexpected local error: %v", err)
		}
		if localSpec.Kit != DefaultBasementKitRef {
			t.Fatalf("local legacy Kit = %q, want %q", localSpec.Kit, DefaultBasementKitRef)
		}

		managedSpec, err := convertUIConfigToSpec(map[string]interface{}{
			"name":     "legacy-managed",
			"stackkit": "base-kit",
			"context":  "cloud",
			"nodes": []interface{}{
				map[string]interface{}{"name": "main", "role": "standalone"},
			},
			"metadata": map[string]interface{}{
				"provider_id":          "ionos",
				"provider_region":      "us-ewr",
				"stackkit_catalog_ref": "base-kit",
			},
		})
		if err != nil {
			t.Fatalf("unexpected managed error: %v", err)
		}
		if managedSpec.Kit != DefaultCloudKitRef {
			t.Fatalf("managed legacy Kit = %q, want %q", managedSpec.Kit, DefaultCloudKitRef)
		}
		if managedSpec.Metadata[metadataKeyStackKitCatalogRef] != DefaultCloudKitRef {
			t.Fatalf("managed stackkit_catalog_ref = %q, want %q", managedSpec.Metadata[metadataKeyStackKitCatalogRef], DefaultCloudKitRef)
		}
		if managedSpec.Metadata[metadataKeyIONOSDatacenter] != "us/ewr" || managedSpec.Metadata[metadataKeyProviderRegion] != "us/ewr" {
			t.Fatalf("managed IONOS metadata = %+v, want us/ewr", managedSpec.Metadata)
		}
	})

	t.Run("StackKits cloud kombify-me address mode maps to kombify.me domain", func(t *testing.T) {
		config := map[string]interface{}{
			"name":     "cloud-stack",
			"stackkit": "cloud-kit",
			"context":  "cloud",
			"network": map[string]interface{}{
				"mode": "public",
			},
			"nodes": []interface{}{
				map[string]interface{}{"name": "main", "role": "standalone"},
			},
			"metadata": map[string]interface{}{
				"address_mode": "kombify-me",
				"provider_id":  "centron",
			},
		}

		spec, err := convertUIConfigToSpec(config)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if spec.Network.Domain != "kombify.me" {
			t.Fatalf("Network.Domain = %q, want kombify.me", spec.Network.Domain)
		}
		if spec.Metadata["subdomain_prefix"] != "" {
			t.Fatalf("Metadata[subdomain_prefix] = %q, want empty until kombify.me registration", spec.Metadata["subdomain_prefix"])
		}
	})

	t.Run("StackKits cloud kombify-me does not invent an unregistered prefix", func(t *testing.T) {
		config := map[string]interface{}{
			"name":     "e2e-centron-kombify-me-20260525120045-cloud-centron",
			"stackkit": "cloud-kit",
			"context":  "cloud",
			"network": map[string]interface{}{
				"mode": "public",
			},
			"nodes": []interface{}{
				map[string]interface{}{"name": "main", "role": "standalone"},
			},
			"metadata": map[string]interface{}{
				"address_mode": "kombify-me",
				"provider_id":  "centron",
			},
		}

		spec, err := convertUIConfigToSpec(config)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if prefix := spec.Metadata["subdomain_prefix"]; prefix != "" {
			t.Fatalf("Metadata[subdomain_prefix] = %q, want empty until Kombify-Me returns the real registered base", prefix)
		}
	})
}

// TestMapOptionsToServices_AllFlags tests all enable_* flags.
func TestMapOptionsToServices_AllFlags(t *testing.T) {
	tests := []struct {
		name        string
		options     map[string]interface{}
		wantService string
		wantType    string
	}{
		{
			name:        "enable_headscale",
			options:     map[string]interface{}{"enable_headscale": true},
			wantService: "headscale",
			wantType:    "vpn",
		},
		{
			name:        "enable_pocket_id",
			options:     map[string]interface{}{"enable_pocket_id": true},
			wantService: "pocket-id",
			wantType:    "auth",
		},
		{
			name:        "enable_pocketbase_backend",
			options:     map[string]interface{}{"enable_pocketbase_backend": true},
			wantService: "pocketbase",
			wantType:    "backend",
		},
		{
			name:        "legacy enable_pocketbase remains backend",
			options:     map[string]interface{}{"enable_pocketbase": true},
			wantService: "pocketbase",
			wantType:    "backend",
		},
		{
			name: "pocketbase identity with passkeys adds pocket id",
			options: map[string]interface{}{
				"identity_head":     "pocketbase",
				"requires_passkeys": true,
			},
			wantService: "pocket-id",
			wantType:    "auth",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			services := mapOptionsToServices(tt.options)

			found := false
			for _, svc := range services {
				if svc.Name == tt.wantService {
					found = true
					if svc.Type != tt.wantType {
						t.Errorf("expected type %q for service %q, got %q", tt.wantType, tt.wantService, svc.Type)
					}
					break
				}
			}
			if !found {
				t.Errorf("expected service %q not found", tt.wantService)
			}
		})
	}
}

func TestMapOptionsToServices_IdentityHeadMatrix(t *testing.T) {
	tests := []struct {
		name     string
		options  map[string]interface{}
		expected map[string]string
	}{
		{
			name: "default pocket id identity",
			options: map[string]interface{}{
				"identity_head": "pocket_id",
			},
			expected: map[string]string{
				"pocket-id": "auth",
			},
		},
		{
			name: "pocketbase as identity head",
			options: map[string]interface{}{
				"identity_head": "pocketbase",
			},
			expected: map[string]string{
				"pocketbase": "backend",
			},
		},
		{
			name: "pocketbase backend plus passkeys",
			options: map[string]interface{}{
				"enable_pocketbase_backend": true,
				"requires_passkeys":         true,
			},
			expected: map[string]string{
				"pocketbase": "backend",
				"pocket-id":  "auth",
			},
		},
		{
			name: "explicit pocket id and pocketbase are not duplicated",
			options: map[string]interface{}{
				"enable_pocketbase":         true,
				"enable_pocketbase_backend": true,
				"enable_pocket_id":          true,
				"identity_head":             "pocket_id",
			},
			expected: map[string]string{
				"pocketbase": "backend",
				"pocket-id":  "auth",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			services := mapOptionsToServices(tt.options)

			if len(services) != len(tt.expected) {
				t.Fatalf("mapOptionsToServices() returned %d services, want %d: %#v", len(services), len(tt.expected), services)
			}

			for name, wantType := range tt.expected {
				found := false
				for _, svc := range services {
					if svc.Name != name {
						continue
					}
					found = true
					if svc.Type != wantType {
						t.Errorf("service %q type = %q, want %q", name, svc.Type, wantType)
					}
				}
				if !found {
					t.Errorf("expected service %q not found in %#v", name, services)
				}
			}
		})
	}
}

// TestMapServiceNameToType_FullCoverage tests more service name mappings.
func TestMapServiceNameToType_FullCoverage(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		// Databases
		{"postgres", "database"},
		{"mysql", "database"},
		{"mariadb", "database"},
		{"mongodb", "database"},
		{"redis", "database"},
		// PaaS
		{"caprover", "paas"},
		{"coolify", "paas"},
		{"dokku", "paas"},
		{"portainer", "paas"},
		// Monitoring
		{"loki", "monitoring"},
		{"uptime-kuma", "monitoring"},
		{"uptime_kuma", "monitoring"},
		{"uptime", "monitoring"},
		// Storage
		{"minio", "storage"},
		{"seafile", "storage"},
		// Auth
		{"keycloak", "auth"},
		{"authentik", "auth"},
		{"authelia", "auth"},
		{"pocket-id", "auth"},
		{"pocket_id", "auth"},
		{"pocketid", "auth"},
		{"tinyauth", "auth"},
		// Backup
		{"restic", "backup"},
		{"borg", "backup"},
		// Custom/Media
		{"jellyfin", "custom"},
		{"plex", "custom"},
		// Case insensitivity
		{"TRAEFIK", "reverse-proxy"},
		{"OTEL-Collector", "monitoring"},
		{"VictoriaMetrics", "monitoring"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mapServiceNameToType(tt.name)
			if got != tt.want {
				t.Errorf("mapServiceNameToType(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}
