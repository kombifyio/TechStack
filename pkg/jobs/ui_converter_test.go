// Package jobs provides unit tests for UI converter functions.
package jobs

import (
	"errors"
	"testing"

	"github.com/kombifyio/techstack/internal/providercatalog"
)

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

	t.Run("legacy homelab provider stays on the local runtime path", func(t *testing.T) {
		spec, err := convertUIConfigToSpec(map[string]interface{}{
			"name":     "legacy-stack",
			"provider": "homelab",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		localProvider := false
		for _, node := range spec.Nodes {
			localProvider = localProvider || node.Provider == "local"
		}
		if !localProvider {
			t.Fatalf("nodes = %#v, want a local runtime provider", spec.Nodes)
		}
	})

	t.Run("wizard access modes produce their network policy", func(t *testing.T) {
		cases := []struct {
			name       string
			accessMode string
			wantVPN    string
			wantDomain string
		}{
			{name: "local", accessMode: "local", wantVPN: "none"},
			{name: "anywhere", accessMode: "anywhere", wantVPN: "tailscale", wantDomain: "ts.net"},
			{name: "public", accessMode: "public", wantVPN: "none"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				spec, err := convertUIConfigToSpec(map[string]interface{}{
					"name":     "network-stack",
					"provider": "local",
					"network": map[string]interface{}{
						"accessMode": tc.accessMode,
					},
				})
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if spec.Network.VPN != tc.wantVPN || spec.Network.Domain != tc.wantDomain {
					t.Fatalf("network = %#v, want VPN %q and domain %q", spec.Network, tc.wantVPN, tc.wantDomain)
				}
			})
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

	t.Run("service options reach the generated spec", func(t *testing.T) {
		spec, err := convertUIConfigToSpec(map[string]interface{}{
			"name":     "option-stack",
			"provider": "local",
			"options": map[string]interface{}{
				"enable_traefik":            true,
				"enable_pocketbase_backend": true,
				"enable_pocket_id":          true,
				"enable_headscale":          true,
				"enable_monitoring":         true,
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		services := map[string]string{}
		for _, service := range spec.Services {
			services[service.Name] = service.Type
		}
		for name, wantType := range map[string]string{
			"traefik":        "reverse-proxy",
			"pocketbase":     "backend",
			"pocket-id":      "auth",
			"headscale":      "vpn",
			"otel-collector": "monitoring",
		} {
			if services[name] != wantType {
				t.Fatalf("service %q type = %q, want %q", name, services[name], wantType)
			}
		}
	})

	t.Run("PocketBase passkeys include the Pocket ID authority", func(t *testing.T) {
		spec, err := convertUIConfigToSpec(map[string]interface{}{
			"name":     "passkey-stack",
			"provider": "local",
			"options": map[string]interface{}{
				"identity_head":     "pocketbase",
				"requires_passkeys": true,
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		services := map[string]string{}
		for _, service := range spec.Services {
			services[service.Name] = service.Type
		}
		if services["pocketbase"] != "backend" || services["pocket-id"] != "auth" {
			t.Fatalf("services = %#v, want PocketBase backend plus Pocket ID auth", services)
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

	t.Run("explicit kits stay on the named product", func(t *testing.T) {
		localSpec, err := convertUIConfigToSpec(map[string]interface{}{
			"name":     "local-stack",
			"stackkit": "basement-kit",
			"context":  "local",
			"nodes": []interface{}{
				map[string]interface{}{"name": "main", "role": "standalone"},
			},
		})
		if err != nil {
			t.Fatalf("unexpected local error: %v", err)
		}
		if localSpec.Kit != DefaultBasementKitRef {
			t.Fatalf("local Kit = %q, want %q", localSpec.Kit, DefaultBasementKitRef)
		}

		managedSpec, err := convertUIConfigToSpec(map[string]interface{}{
			"name":     "managed-stack",
			"stackkit": "cloud-kit",
			"context":  "cloud",
			"nodes": []interface{}{
				map[string]interface{}{"name": "main", "role": "standalone"},
			},
			"metadata": map[string]interface{}{
				"provider_id":          "ionos",
				"provider_region":      "us-ewr",
				"stackkit_catalog_ref": "cloud-kit",
			},
		})
		if err != nil {
			t.Fatalf("unexpected managed error: %v", err)
		}
		if managedSpec.Kit != DefaultCloudKitRef {
			t.Fatalf("managed Kit = %q, want %q", managedSpec.Kit, DefaultCloudKitRef)
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

}
