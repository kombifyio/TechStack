package unifier

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kombifyio/techstack/pkg/core"
)

func TestLoader_LoadBytes_Valid(t *testing.T) {
	loader := NewLoader()

	yaml := []byte(`
name: test-stack
kit: basement-kit
nodes:
  - name: main-server
    type: main
    provider: local
    ssh:
      host: 192.168.1.100
      user: ubuntu
services:
  - name: traefik
    type: reverse-proxy
    node: main-server
`)

	spec, err := loader.LoadBytes(yaml)
	if err != nil {
		t.Fatalf("LoadBytes() failed: %v", err)
	}

	if spec.Name != "test-stack" {
		t.Errorf("expected name 'test-stack', got '%s'", spec.Name)
	}

	if spec.Kit != "basement-kit" {
		t.Errorf("expected kit 'basement-kit', got '%s'", spec.Kit)
	}

	if len(spec.Nodes) != 1 {
		t.Errorf("expected 1 node, got %d", len(spec.Nodes))
	}

	if len(spec.Services) != 1 {
		t.Errorf("expected 1 service, got %d", len(spec.Services))
	}
}

func TestLoader_LoadBytes_StackKitsStackSpec(t *testing.T) {
	loader := NewLoader()

	yaml := []byte(`
name: release-stack
stackkit: basement-kit
mode: simple
runtime: docker
context: cloud
domain: kombify.me
network:
  mode: public
vpn:
  enabled: true
  type: headscale
ssh:
  user: root
  port: 22
nodes:
  - name: main
    role: standalone
services:
  pocketid:
    enabled: true
  vaultwarden:
    enabled: true
  jellyfin:
    enabled: false
metadata:
  provider_id: centron
`)

	spec, err := loader.LoadBytes(yaml)
	if err != nil {
		t.Fatalf("LoadBytes() failed: %v", err)
	}

	if spec.Kit != "basement-kit" {
		t.Fatalf("Kit = %q, want basement-kit", spec.Kit)
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
	if len(spec.Services) != 2 {
		t.Fatalf("services = %#v, want enabled map entries only", spec.Services)
	}
	serviceTypes := map[string]string{}
	for _, service := range spec.Services {
		serviceTypes[service.Name] = service.Type
	}
	if serviceTypes["pocket-id"] != "auth" {
		t.Fatalf("pocketid alias should normalize to auth service, got %#v", spec.Services)
	}
	if serviceTypes["vaultwarden"] != "auth" {
		t.Fatalf("vaultwarden should infer auth type, got %#v", spec.Services)
	}
}

func TestLoader_LoadInputBytesRejectsLegacyManagedProviderWrites(t *testing.T) {
	tests := map[string]string{
		"legacy lease provider field": `metadata:
  lease_provider: ionos-managed
`,
		"legacy simulate provider field": `metadata:
  simulate_provider_id: ionos-managed
`,
		"composite provider id": `metadata:
  provider_id: ionos-managed
`,
		"composite node provider": `nodes:
  - name: main
    provider: centron-managed
`,
		"managed node cannot replace provider id": `metadata:
  server_mode: monthly-runtime
nodes:
  - name: main
    provider: ionos
`,
		"provider id case is not normalized": `metadata:
  provider_id: IONOS
`,
	}

	loader := NewLoader()
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := loader.LoadInputBytes([]byte(input)); err == nil {
				t.Fatal("LoadInputBytes() error = nil, want fail-closed provider identity rejection")
			}
		})
	}
}

func TestLoaderLoadInputBytesKeepsUnmanagedInfrastructureProviders(t *testing.T) {
	_, err := NewLoader().LoadInputBytes([]byte(`context: cloud
nodes:
  - name: main
    provider: hetzner
`))
	if err != nil {
		t.Fatalf("LoadInputBytes() rejected cloud-context unmanaged Hetzner provider: %v", err)
	}
}

func TestPipelinePreValidateAcceptsWizardStackSpecServiceToggles(t *testing.T) {
	loader := NewLoader()

	input, err := loader.LoadInputBytes([]byte(`{
  "name": "techstack",
  "stackkit": "basement-kit",
  "mode": "simple",
  "runtime": "docker",
  "context": "local",
  "domain": "stack.home",
  "network": { "mode": "local" },
  "nodes": [{ "name": "main", "role": "standalone" }],
  "services": {
    "homepage": { "enabled": true },
    "whoami": { "enabled": true },
    "tinyauth": { "enabled": true },
    "dokploy": { "enabled": true },
    "uptime_kuma": { "enabled": true },
    "pocketid": { "enabled": true },
    "vaultwarden": { "enabled": true },
    "immich": { "enabled": true },
    "files": { "enabled": false }
  }
}`))
	if err != nil {
		t.Fatalf("LoadInputBytes() failed: %v", err)
	}

	spec := NormalizeInputSpec(input)
	serviceTypes := map[string]string{}
	for _, service := range spec.Services {
		serviceTypes[service.Name] = service.Type
	}

	wantTypes := map[string]string{
		"homepage":    "service",
		"whoami":      "service",
		"tinyauth":    "auth",
		"dokploy":     "paas",
		"uptime-kuma": "monitoring",
		"pocket-id":   "auth",
		"vaultwarden": "auth",
		"immich":      "media",
	}
	for name, wantType := range wantTypes {
		if serviceTypes[name] != wantType {
			t.Fatalf("service %q type = %q, want %q; services=%#v", name, serviceTypes[name], wantType, spec.Services)
		}
	}
	if _, exists := serviceTypes["files"]; exists {
		t.Fatalf("disabled service should be omitted, got %#v", spec.Services)
	}

	engine, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	validation, err := NewPipeline(engine).PreValidate(spec)
	if err != nil {
		t.Fatalf("PreValidate() failed: %v", err)
	}
	if !validation.Valid {
		t.Fatalf("wizard StackSpec toggles should be valid, got errors: %#v", validation.Errors)
	}
}

func TestNormalizeInputSpec_PreservesStackKitsCompatibilityEdges(t *testing.T) {
	stackKit := "fallback-kit"
	domain := "example.test"
	networkVPN := "wireguard"
	vpnType := "headscale"
	vpnEnabled := false
	context := "cloud"
	subdomainPrefix := "ts-live-a-ionos"
	globalHost := "global.example.test"
	globalPort := 2222
	globalUser := "ubuntu"
	globalKeyPath := "/keys/global"
	nodeName := "main"
	nodeRole := "control-plane"
	nodeIP := "10.0.0.2"
	serviceName := "api"
	serviceType := "container"
	serviceNeed := "db"

	input := &core.InputSpec{
		StackKit:        &stackKit,
		Context:         &context,
		Domain:          &domain,
		SubdomainPrefix: &subdomainPrefix,
		Network: &core.InputNetworkSpec{
			VPN: &networkVPN,
		},
		SSH: &core.InputSSHConfig{
			Host:    &globalHost,
			Port:    &globalPort,
			User:    &globalUser,
			KeyPath: &globalKeyPath,
		},
		VPN: &core.InputVPNSpec{
			Enabled: &vpnEnabled,
			Type:    &vpnType,
		},
		Metadata: map[string]string{
			"provider_id": "ionos",
		},
		Nodes: []core.InputNodeSpec{
			{
				Name: &nodeName,
				Role: &nodeRole,
				IP:   &nodeIP,
				Tags: map[string]string{"role": "primary"},
			},
		},
		Services: core.InputServiceSpecs{
			{
				Name:  &serviceName,
				Type:  &serviceType,
				Node:  &nodeName,
				Needs: []string{serviceNeed},
			},
		},
	}

	spec := NormalizeInputSpec(input)

	if spec.Kit != stackKit {
		t.Fatalf("Kit = %q, want StackKit fallback %q", spec.Kit, stackKit)
	}
	if spec.Network.Domain != domain {
		t.Fatalf("Network.Domain = %q, want %q", spec.Network.Domain, domain)
	}
	if spec.Metadata["subdomain_prefix"] != subdomainPrefix {
		t.Fatalf("Metadata[subdomain_prefix] = %q, want %q", spec.Metadata["subdomain_prefix"], subdomainPrefix)
	}
	if spec.Network.VPN != "none" {
		t.Fatalf("Network.VPN = %q, want none when vpn.enabled is false", spec.Network.VPN)
	}
	if len(spec.Nodes) != 1 {
		t.Fatalf("Nodes length = %d, want 1", len(spec.Nodes))
	}
	node := spec.Nodes[0]
	if node.Type != "main" {
		t.Fatalf("Node.Type = %q, want role-mapped main", node.Type)
	}
	if node.Provider != "ionos" {
		t.Fatalf("Node.Provider = %q, want provider_id", node.Provider)
	}
	if node.SSH == nil {
		t.Fatal("Node.SSH should be synthesized from node host/IP and global defaults")
	}
	if node.SSH.Host != nodeIP || node.SSH.Port != globalPort || node.SSH.User != globalUser || node.SSH.KeyPath != globalKeyPath {
		t.Fatalf("unexpected SSH config: %#v", node.SSH)
	}
	if len(spec.Services) != 1 || len(spec.Services[0].Needs) != 1 || spec.Services[0].Needs[0] != serviceNeed {
		t.Fatalf("unexpected services: %#v", spec.Services)
	}

	input.Metadata["provider_id"] = "changed"
	input.Nodes[0].Tags["role"] = "changed"
	input.Services[0].Needs[0] = "changed"

	if spec.Metadata["provider_id"] != "ionos" {
		t.Fatalf("metadata was not deep-copied: %#v", spec.Metadata)
	}
	if spec.Nodes[0].Tags["role"] != "primary" {
		t.Fatalf("node tags were not deep-copied: %#v", spec.Nodes[0].Tags)
	}
	if spec.Services[0].Needs[0] != serviceNeed {
		t.Fatalf("service needs were not deep-copied: %#v", spec.Services[0].Needs)
	}
}

func TestNormalizeInputSpec_HostlessGlobalSSHDefaultsStayNil(t *testing.T) {
	user := "root"
	port := 22
	nodeName := "main"

	spec := NormalizeInputSpec(&core.InputSpec{
		SSH: &core.InputSSHConfig{
			User: &user,
			Port: &port,
		},
		Nodes: []core.InputNodeSpec{{Name: &nodeName}},
	})

	if len(spec.Nodes) != 1 {
		t.Fatalf("Nodes length = %d, want 1", len(spec.Nodes))
	}
	if spec.Nodes[0].SSH != nil {
		t.Fatalf("hostless global SSH defaults should stay nil, got %#v", spec.Nodes[0].SSH)
	}
}

func TestNormalizeInputSpec_NetworkDomainPrecedence(t *testing.T) {
	tests := []struct {
		name  string
		input *core.InputSpec
		want  string
	}{
		{
			name: "network domain beats top-level domain and mode",
			input: &core.InputSpec{
				Domain: testPtr("top.example.test"),
				Network: &core.InputNetworkSpec{
					Domain: testPtr("network.example.test"),
					Mode:   testPtr("local"),
				},
			},
			want: "network.example.test",
		},
		{
			name: "top-level domain beats network mode",
			input: &core.InputSpec{
				Domain: testPtr("top.example.test"),
				Network: &core.InputNetworkSpec{
					Mode: testPtr("local"),
				},
			},
			want: "top.example.test",
		},
		{
			name: "network mode alone does not become a local domain",
			input: &core.InputSpec{
				Network: &core.InputNetworkSpec{
					Mode: testPtr("local"),
				},
			},
			want: "",
		},
		{
			name: "legacy domain-shaped network mode remains compatibility fallback",
			input: &core.InputSpec{
				Network: &core.InputNetworkSpec{
					Mode: testPtr("legacy.local"),
				},
			},
			want: "legacy.local",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := NormalizeInputSpec(tt.input)
			if spec.Network.Domain != tt.want {
				t.Fatalf("Network.Domain = %q, want %q", spec.Network.Domain, tt.want)
			}
		})
	}
}

func TestNormalizeInputSpec_ProviderPrecedence(t *testing.T) {
	tests := []struct {
		name  string
		input *core.InputSpec
		want  string
	}{
		{
			name: "explicit node provider wins",
			input: &core.InputSpec{
				Context:  testPtr("cloud"),
				Metadata: map[string]string{"provider_id": "centron"},
				Nodes: []core.InputNodeSpec{
					{Name: testPtr("main"), Provider: testPtr("explicit-provider")},
				},
			},
			want: "explicit-provider",
		},
		{
			name: "provider id supplies managed provider",
			input: &core.InputSpec{
				Metadata: map[string]string{"provider_id": "ionos"},
				Nodes:    []core.InputNodeSpec{{Name: testPtr("main")}},
			},
			want: "ionos",
		},
		{
			name: "cloud context has no implicit vendor default",
			input: &core.InputSpec{
				Context: testPtr("cloud"),
				Nodes:   []core.InputNodeSpec{{Name: testPtr("main")}},
			},
			want: "local",
		},
		{
			name: "local default is fallback",
			input: &core.InputSpec{
				Nodes: []core.InputNodeSpec{{Name: testPtr("main")}},
			},
			want: "local",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := NormalizeInputSpec(tt.input)
			if len(spec.Nodes) != 1 {
				t.Fatalf("Nodes length = %d, want 1", len(spec.Nodes))
			}
			if spec.Nodes[0].Provider != tt.want {
				t.Fatalf("Node.Provider = %q, want %q", spec.Nodes[0].Provider, tt.want)
			}
		})
	}
}

func TestNormalizeInputSpec_ExplicitNodeSSHAllowsHostlessConfig(t *testing.T) {
	spec := NormalizeInputSpec(&core.InputSpec{
		Nodes: []core.InputNodeSpec{
			{
				Name: testPtr("main"),
				SSH:  &core.InputSSHConfig{User: testPtr("ubuntu")},
			},
		},
	})

	if len(spec.Nodes) != 1 {
		t.Fatalf("Nodes length = %d, want 1", len(spec.Nodes))
	}
	if spec.Nodes[0].SSH == nil {
		t.Fatal("explicit node SSH should create a config even when host is empty")
	}
	if spec.Nodes[0].SSH.Host != "" || spec.Nodes[0].SSH.User != "ubuntu" {
		t.Fatalf("unexpected SSH config: %#v", spec.Nodes[0].SSH)
	}
}

func TestLoader_LoadBytes_MissingName(t *testing.T) {
	loader := NewLoader()

	yaml := []byte(`
kit: basement-kit
nodes:
  - name: main-server
    type: main
    provider: local
`)

	_, err := loader.LoadBytes(yaml)
	if err != nil {
		t.Fatalf("LoadBytes() should not fail for missing name (Easy-Path): %v", err)
	}
}

func TestLoader_LoadBytes_EmptySpec(t *testing.T) {
	loader := NewLoader()

	yaml := []byte(`
{}`)

	_, err := loader.LoadBytes(yaml)
	if err != nil {
		t.Fatalf("LoadBytes() should not fail for empty spec (Easy-Path): %v", err)
	}
}

func TestLoader_LoadBytes_MissingKit(t *testing.T) {
	loader := NewLoader()

	yaml := []byte(`
name: test-stack
nodes:
  - name: main-server
    type: main
    provider: local
`)

	// Kit is optional for Easy-Path users - the Pipeline will auto-select
	spec, err := loader.LoadBytes(yaml)
	if err != nil {
		t.Fatalf("LoadBytes() should not fail for missing kit (Easy-Path): %v", err)
	}

	if spec.Kit != "" {
		t.Errorf("expected empty kit, got '%s'", spec.Kit)
	}
}

func TestLoader_LoadBytes_EmptyNodes(t *testing.T) {
	loader := NewLoader()

	yaml := []byte(`
name: test-stack
kit: basement-kit
nodes: []
`)

	_, err := loader.LoadBytes(yaml)
	if err != nil {
		t.Fatalf("LoadBytes() should not fail for empty nodes (Easy-Path): %v", err)
	}
}

func TestLoader_LoadBytes_DuplicateNodeNames(t *testing.T) {
	loader := NewLoader()

	yaml := []byte(`
name: test-stack
kit: basement-kit
nodes:
  - name: server-1
    type: main
    provider: local
  - name: server-1
    type: worker
    provider: local
`)

	_, err := loader.LoadBytes(yaml)
	if err == nil {
		t.Fatal("expected error for duplicate node names")
	}
}

func TestLoader_LoadBytes_DuplicateServiceNames(t *testing.T) {
	loader := NewLoader()

	yaml := []byte(`
name: test-stack
kit: basement-kit
nodes:
  - name: server-1
    type: main
    provider: local
services:
  - name: traefik
    type: reverse-proxy
  - name: traefik
    type: monitoring
`)

	_, err := loader.LoadBytes(yaml)
	if err == nil {
		t.Fatal("expected error for duplicate service names")
	}
}

func TestLoader_LoadBytes_InvalidNodeReference(t *testing.T) {
	loader := NewLoader()

	yaml := []byte(`
name: test-stack
kit: basement-kit
nodes:
  - name: server-1
    type: main
    provider: local
services:
  - name: traefik
    type: reverse-proxy
    node: nonexistent-server
`)

	_, err := loader.LoadBytes(yaml)
	if err == nil {
		t.Fatal("expected error for invalid node reference")
	}
}

func TestLoader_ToYAML(t *testing.T) {
	loader := NewLoader()

	spec := &core.KombinationSpec{
		Name: "test-stack",
		Kit:  "basement-kit",
		Nodes: []core.NodeSpec{
			{
				Name:     "main-server",
				Type:     "main",
				Provider: "local",
			},
		},
	}

	data, err := loader.ToYAML(spec)
	if err != nil {
		t.Fatalf("ToYAML() failed: %v", err)
	}

	if len(data) == 0 {
		t.Fatal("ToYAML() returned empty data")
	}

	// Verify we can parse it back
	spec2, err := loader.LoadBytes(data)
	if err != nil {
		t.Fatalf("Round-trip failed: %v", err)
	}

	if spec2.Name != spec.Name {
		t.Errorf("Round-trip name mismatch: %s vs %s", spec2.Name, spec.Name)
	}
}

func TestLoader_SaveFile_LoadFile(t *testing.T) {
	loader := NewLoader()

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "kombination.yaml")

	spec := &core.KombinationSpec{
		Name: "test-stack",
		Kit:  "basement-kit",
		Nodes: []core.NodeSpec{
			{
				Name:     "main-server",
				Type:     "main",
				Provider: "local",
			},
		},
	}

	// Save
	err := loader.SaveFile(spec, filePath)
	if err != nil {
		t.Fatalf("SaveFile() failed: %v", err)
	}

	// Verify file exists
	if _, statErr := os.Stat(filePath); os.IsNotExist(statErr) {
		t.Fatal("SaveFile() did not create file")
	}

	// Load back
	spec2, err := loader.LoadFile(filePath)
	if err != nil {
		t.Fatalf("LoadFile() failed: %v", err)
	}

	if spec2.Name != spec.Name {
		t.Errorf("Round-trip name mismatch: %s vs %s", spec2.Name, spec.Name)
	}
}

func TestLoader_LoadFile_NotFound(t *testing.T) {
	loader := NewLoader()

	_, err := loader.LoadFile("/nonexistent/path/kombination.yaml")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

// =============================================================================
// IntentSpec Tests
// =============================================================================

func TestLoader_LoadIntentBytes(t *testing.T) {
	loader := NewLoader()

	t.Run("valid intent", func(t *testing.T) {
		yamlData := []byte(`
name: my-homelab
version: "1.0"
kit: basement-kit
goals:
  storage: true
  media: true
network:
  mode: local
  vpn: headscale
variant: default
`)
		intent, hash, err := loader.LoadIntentBytes(yamlData)
		if err != nil {
			t.Fatalf("LoadIntentBytes failed: %v", err)
		}

		if intent.Name != "my-homelab" {
			t.Errorf("Name = %q, want %q", intent.Name, "my-homelab")
		}
		if intent.Kit != "basement-kit" {
			t.Errorf("Kit = %q, want %q", intent.Kit, "basement-kit")
		}
		if hash == "" {
			t.Error("hash should not be empty")
		}
		if intent.APIVersion != "techstack/v1" {
			t.Errorf("APIVersion = %q, want %q", intent.APIVersion, "techstack/v1")
		}
		if intent.Kind != "IntentSpec" {
			t.Errorf("Kind = %q, want %q", intent.Kind, "IntentSpec")
		}
	})

	t.Run("missing name returns error", func(t *testing.T) {
		yamlData := []byte(`
version: "1.0"
kit: basement-kit
`)
		_, _, err := loader.LoadIntentBytes(yamlData)
		if err == nil {
			t.Error("expected error for missing name")
		}
	})

	t.Run("invalid yaml returns error", func(t *testing.T) {
		yamlData := []byte(`
name: test
invalid: [unclosed
`)
		_, _, err := loader.LoadIntentBytes(yamlData)
		if err == nil {
			t.Error("expected error for invalid YAML")
		}
	})

	t.Run("hash is deterministic", func(t *testing.T) {
		yamlData := []byte(`name: test-stack`)
		_, hash1, _ := loader.LoadIntentBytes(yamlData)
		_, hash2, _ := loader.LoadIntentBytes(yamlData)

		if hash1 != hash2 {
			t.Error("same input should produce same hash")
		}
	})

	t.Run("goals parsed correctly", func(t *testing.T) {
		yamlData := []byte(`
name: test
goals:
  storage: true
  media: false
  development: true
  monitoring: true
`)
		intent, _, err := loader.LoadIntentBytes(yamlData)
		if err != nil {
			t.Fatalf("LoadIntentBytes failed: %v", err)
		}

		if intent.Goals == nil {
			t.Fatal("Goals should not be nil")
		}
		if !intent.Goals.Storage {
			t.Error("Storage should be true")
		}
		if intent.Goals.Media {
			t.Error("Media should be false")
		}
		if !intent.Goals.Development {
			t.Error("Development should be true")
		}
	})

	t.Run("overrides parsed correctly", func(t *testing.T) {
		yamlData := []byte(`
name: test
overrides:
  kit: custom-kit
  services:
    - traefik
    - caprover
`)
		intent, _, err := loader.LoadIntentBytes(yamlData)
		if err != nil {
			t.Fatalf("LoadIntentBytes failed: %v", err)
		}

		if intent.Overrides == nil {
			t.Fatal("Overrides should not be nil")
		}
		if intent.Overrides.Kit != "custom-kit" {
			t.Errorf("Override Kit = %q, want %q", intent.Overrides.Kit, "custom-kit")
		}
		if len(intent.Overrides.Services) != 2 {
			t.Errorf("Override Services length = %d, want %d", len(intent.Overrides.Services), 2)
		}
	})
}

func TestLoader_LoadIntentFile(t *testing.T) {
	loader := NewLoader()
	tmpDir := t.TempDir()

	t.Run("loads file successfully", func(t *testing.T) {
		filePath := filepath.Join(tmpDir, "kombination.yaml")
		content := []byte(`
name: file-test-stack
version: "2.0"
kit: modern-homelab
`)
		if err := os.WriteFile(filePath, content, 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		intent, hash, err := loader.LoadIntentFile(filePath)
		if err != nil {
			t.Fatalf("LoadIntentFile failed: %v", err)
		}

		if intent.Name != "file-test-stack" {
			t.Errorf("Name = %q, want %q", intent.Name, "file-test-stack")
		}
		if hash == "" {
			t.Error("hash should not be empty")
		}
	})

	t.Run("non-existent file returns error", func(t *testing.T) {
		_, _, err := loader.LoadIntentFile("/non/existent/file.yaml")
		if err == nil {
			t.Error("expected error for non-existent file")
		}
	})
}

func TestLoader_ConvertIntentToKombination(t *testing.T) {
	loader := NewLoader()

	t.Run("nil intent returns nil", func(t *testing.T) {
		result := loader.ConvertIntentToKombination(nil)
		if result != nil {
			t.Error("nil intent should return nil")
		}
	})

	t.Run("basic conversion", func(t *testing.T) {
		intent := &core.IntentSpec{
			Name:    "test-stack",
			Version: "1.0",
			Kit:     "basement-kit",
		}

		spec := loader.ConvertIntentToKombination(intent)

		if spec.Name != "test-stack" {
			t.Errorf("Name = %q, want %q", spec.Name, "test-stack")
		}
		if spec.Version != "1.0" {
			t.Errorf("Version = %q, want %q", spec.Version, "1.0")
		}
		if spec.Kit != "basement-kit" {
			t.Errorf("Kit = %q, want %q", spec.Kit, "basement-kit")
		}
	})

	t.Run("override kit applied", func(t *testing.T) {
		intent := &core.IntentSpec{
			Name: "test",
			Kit:  "basement-kit",
			Overrides: &core.IntentOverrides{
				Kit: "custom-kit",
			},
		}

		spec := loader.ConvertIntentToKombination(intent)

		if spec.Kit != "custom-kit" {
			t.Errorf("Kit should be overridden, got %q", spec.Kit)
		}
	})

	t.Run("network converted", func(t *testing.T) {
		intent := &core.IntentSpec{
			Name: "test",
			Network: &core.IntentNetwork{
				Mode: "local",
				VPN:  "headscale",
			},
		}

		spec := loader.ConvertIntentToKombination(intent)

		if spec.Network.VPN != "headscale" {
			t.Errorf("VPN = %q, want %q", spec.Network.VPN, "headscale")
		}
	})
}

func testPtr[T any](value T) *T {
	return &value
}
